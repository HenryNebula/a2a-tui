package a2ui

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"
)

// Fn is a renderer-side function implementation. Args arrive already
// evaluated (nested dynamics resolved); implementations must not panic on
// malformed input.
type Fn func(args map[string]any, ctx EvalContext) (any, error)

// ErrGated is returned by side-effecting functions (openUrl) whose execution
// must be confirmed by the user before it actually runs. Callers gate the
// retry behind explicit confirmation.
var ErrGated = errors.New("a2ui: function is gated behind user confirmation")

// ValidationResult is the v1.0 return shape of validation functions: an
// object with at least a "valid" boolean, optionally code/message/severity.
type ValidationResult map[string]any

// Valid reports whether the result represents a passing check.
func (v ValidationResult) Valid() bool { return toBoolean(v["valid"]) }

// Message returns the human-readable message, if any.
func (v ValidationResult) Message() string {
	s, _ := v["message"].(string)
	return s
}

// FunctionRegistry resolves catalog function calls. The registry ships with
// the basic catalog's 14 functions; more can be added with Register.
type FunctionRegistry struct {
	fns map[string]Fn
}

// NewFunctionRegistry returns a registry preloaded with the basic catalog
// functions (required, regex, length, numeric, email, formatString,
// formatNumber, formatCurrency, formatDate, pluralize, openUrl, and, or,
// not). The system function @index is resolved from the EvalContext and is
// not registry-based.
func NewFunctionRegistry() *FunctionRegistry {
	r := &FunctionRegistry{fns: make(map[string]Fn, 16)}
	r.Register("required", fnRequired)
	r.Register("regex", fnRegex)
	r.Register("length", fnLength)
	r.Register("numeric", fnNumeric)
	r.Register("email", fnEmail)
	r.Register("formatString", fnFormatString)
	r.Register("formatNumber", fnFormatNumber)
	r.Register("formatCurrency", fnFormatCurrency)
	r.Register("formatDate", fnFormatDate)
	r.Register("pluralize", fnPluralize)
	r.Register("openUrl", fnOpenUrl)
	r.Register("and", fnAnd)
	r.Register("or", fnOr)
	r.Register("not", fnNot)
	return r
}

// Register adds or replaces a function.
func (r *FunctionRegistry) Register(name string, fn Fn) {
	if r == nil || r.fns == nil {
		return
	}
	r.fns[name] = fn
}

// Lookup finds a function by name.
func (r *FunctionRegistry) Lookup(name string) (Fn, bool) {
	if r == nil {
		return nil, false
	}
	fn, ok := r.fns[name]
	return fn, ok
}

// Names lists the registered function names in stable order.
func (r *FunctionRegistry) Names() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.fns))
	for name := range r.fns {
		out = append(out, name)
	}
	sortStrings(out)
	return out
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// invalidResult builds a bare failing ValidationResult. The basic
// validators carry no message of their own: the CheckRule's message (the
// agent-provided text) is the display fallback per the spec's contact-form
// example.
func invalidResult() ValidationResult { return ValidationResult{"valid": false} }

// invalidMsg builds a failing ValidationResult with an explicit message.
func invalidMsg(msg string) ValidationResult { return ValidationResult{"valid": false, "message": msg} }

// valid builds a passing ValidationResult.
func valid() ValidationResult { return ValidationResult{"valid": true} }

// fnRequired: true unless the value is null, "", or an empty array.
func fnRequired(args map[string]any, _ EvalContext) (any, error) {
	switch v := args["value"].(type) {
	case nil:
		return invalidResult(), nil
	case string:
		if v == "" {
			return invalidResult(), nil
		}
	case []any:
		if len(v) == 0 {
			return invalidResult(), nil
		}
	}
	return valid(), nil
}

// compileBudget bounds regex compilation work for untrusted patterns.
var compileBudget = regexp.MustCompile(`^[a-zA-Z0-9\s\p{P}\p{S}\\^$]{0,512}$`)

// fnRegex tests value against pattern using Go's RE2 engine. Patterns using
// RE2-unsupported constructs (lookahead, backreferences) fail the check with
// an explanatory message instead of erroring the whole evaluation.
func fnRegex(args map[string]any, _ EvalContext) (any, error) {
	value := Stringify(args["value"])
	pattern, _ := args["pattern"].(string)
	if !compileBudget.MatchString(pattern) {
		return invalidResult(), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return invalidResult(), nil
	}
	if re.MatchString(value) {
		return valid(), nil
	}
	return invalidResult(), nil
}

// fnLength checks string length bounds (min and/or max).
func fnLength(args map[string]any, _ EvalContext) (any, error) {
	value := Stringify(args["value"])
	n := len([]rune(value))
	if raw, ok := args["min"]; ok {
		if min := int(toNumber(raw)); n < min {
			return invalidResult(), nil
		}
	}
	if raw, ok := args["max"]; ok {
		if max := int(toNumber(raw)); n > max {
			return invalidResult(), nil
		}
	}
	return valid(), nil
}

// fnNumeric checks that value parses as a number within optional bounds.
func fnNumeric(args map[string]any, _ EvalContext) (any, error) {
	raw := args["value"]
	f, ok := raw.(float64)
	if !ok {
		s, isStr := raw.(string)
		if !isStr {
			return invalidResult(), nil
		}
		var err error
		f, err = strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return invalidResult(), nil
		}
	}
	if rawMin, ok := args["min"]; ok && f < toNumber(rawMin) {
		return invalidResult(), nil
	}
	if rawMax, ok := args["max"]; ok && f > toNumber(rawMax) {
		return invalidResult(), nil
	}
	return valid(), nil
}

// emailPattern is the standard address check recommended by the
// implementation guide.
var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

func fnEmail(args map[string]any, _ EvalContext) (any, error) {
	value := Stringify(args["value"])
	if emailPattern.MatchString(value) {
		return valid(), nil
	}
	return invalidResult(), nil
}

// fnFormatString interpolates ${...} expressions: absolute/relative data
// paths and named-argument function calls, with \${ escaping.
func fnFormatString(args map[string]any, ctx EvalContext) (any, error) {
	template := Stringify(args["value"])
	child, ok := ctx.child()
	if !ok {
		return nil, fmt.Errorf("a2ui: formatString nesting deeper than %d", maxEvalDepth)
	}
	return interpolate(template, child)
}

// interpolate expands ${...} blocks in s.
func interpolate(s string, ctx EvalContext) (string, error) {
	var out strings.Builder
	out.Grow(len(s))
	i := 0
	for i < len(s) {
		// Escaped "\${" becomes a literal "${".
		if s[i] == '\\' && i+2 < len(s) && s[i+1] == '$' && s[i+2] == '{' {
			out.WriteString("${")
			i += 3
			continue
		}
		if s[i] == '$' && i+1 < len(s) && s[i+1] == '{' {
			end, ok := matchBraces(s, i+1)
			if !ok {
				out.WriteString(s[i:])
				break
			}
			expr := s[i+2 : end]
			val, err := evalExpression(expr, ctx)
			if err != nil {
				return "", fmt.Errorf("a2ui: formatString expression %q: %w", expr, err)
			}
			out.WriteString(Stringify(val))
			i = end + 1
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String(), nil
}

// matchBraces finds the '}' matching the '{' at open, skipping quoted
// strings. Returns the index of the matching brace.
func matchBraces(s string, open int) (int, bool) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = skipQuoted(s, i, '\'')
		case '"':
			i = skipQuoted(s, i, '"')
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

func skipQuoted(s string, start int, quote byte) int {
	for i := start + 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == quote {
			return i
		}
	}
	return len(s)
}

// evalExpression evaluates one ${...} inner expression: a data path, a
// function call with named arguments, or a literal.
func evalExpression(expr string, ctx EvalContext) (any, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, nil
	}
	// Nested ${...} inside an outer expression binds explicitly.
	if strings.HasPrefix(expr, "${") && strings.HasSuffix(expr, "}") {
		return evalExpression(strings.TrimSpace(expr[2:len(expr)-1]), ctx)
	}
	// Function call: identifier '(' ... ')'.
	if idx := strings.IndexByte(expr, '('); idx > 0 && identifierEnd(expr[:idx]) == idx {
		name := expr[:idx]
		if idx+1 >= len(expr) || expr[len(expr)-1] != ')' {
			return nil, fmt.Errorf("unbalanced call to %s", name)
		}
		return evalCallExpr(name, expr[idx+1:len(expr)-1], ctx)
	}
	// Quoted or numeric literal.
	switch expr[0] {
	case '\'', '"':
		if len(expr) >= 2 && expr[len(expr)-1] == expr[0] {
			return strings.ReplaceAll(expr[1:len(expr)-1], "\\"+string(expr[0]), string(expr[0])), nil
		}
		return nil, fmt.Errorf("unterminated string literal %q", expr)
	}
	switch expr {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if f, err := strconv.ParseFloat(expr, 64); err == nil {
		return f, nil
	}
	// Otherwise: a data path (absolute or relative).
	if v, ok := ctx.Resolve(expr); ok {
		return v, nil
	}
	return nil, nil
}

// identifierEnd returns the length of the leading identifier in s.
func identifierEnd(s string) int {
	for i, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '@' {
			continue
		}
		return i
	}
	return len(s)
}

// evalCallExpr evaluates name(arg: value, ...) with literal, path or nested
// ${...} argument values. A single positional nested expression (e.g.
// ${upper(${now()})}) is passed as the "value" argument.
func evalCallExpr(name, argStr string, ctx EvalContext) (any, error) {
	args := map[string]any{}
	argStr = strings.TrimSpace(argStr)
	if argStr != "" {
		trimmed := strings.TrimSpace(argStr)
		if strings.HasPrefix(trimmed, "${") && !strings.Contains(trimmed, ":") {
			// Positional nested expression: bind to the conventional
			// "value" argument name.
			v, err := evalExpression(trimmed, ctx)
			if err != nil {
				return nil, err
			}
			args["value"] = v
		} else {
			var err error
			args, err = parseCallArgs(argStr, ctx)
			if err != nil {
				return nil, err
			}
		}
	}
	call := &Call{Name: name, Args: map[string]any{}}
	// Reuse the standard call path so @index and registry functions behave
	// identically to JSON-declared calls.
	for k, v := range args {
		call.Args[k] = v
	}
	return evaluateCall(call, ctx)
}

func parseCallArgs(s string, ctx EvalContext) (map[string]any, error) {
	out := map[string]any{}
	i := 0
	for i < len(s) {
		// Skip separators and whitespace.
		for i < len(s) && (s[i] == ',' || s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= len(s) {
			break
		}
		colon := strings.IndexByte(s[i:], ':')
		if colon < 0 {
			return nil, fmt.Errorf("expected name: value argument at %q", s[i:])
		}
		name := strings.TrimSpace(s[i : i+colon])
		if name == "" {
			return nil, fmt.Errorf("empty argument name in %q", s)
		}
		j := i + colon + 1
		// The value runs until a top-level comma.
		valStr, next := sliceArgValue(s, j)
		val, err := evalExpression(strings.TrimSpace(valStr), ctx)
		if err != nil {
			return nil, err
		}
		out[name] = val
		i = next
	}
	return out, nil
}

// sliceArgValue returns the raw text of one argument value starting at i and
// the index just past its terminating comma (or end of string).
func sliceArgValue(s string, i int) (string, int) {
	depth := 0
	start := i
	for j := i; j < len(s); j++ {
		switch s[j] {
		case '\'', '"':
			j = skipQuoted(s, j, s[j])
		case '(', '{', '[':
			depth++
		case ')', '}', ']':
			depth--
		case ',':
			if depth == 0 {
				return s[start:j], j + 1
			}
		}
	}
	return s[start:], len(s)
}

// fnFormatNumber renders value with optional decimals and grouping.
func fnFormatNumber(args map[string]any, _ EvalContext) (any, error) {
	value := toNumber(args["value"])
	decimals := -1
	if raw, ok := args["decimals"]; ok {
		decimals = int(toNumber(raw))
	}
	grouping := true
	if raw, ok := args["grouping"]; ok {
		grouping = toBoolean(raw)
	}
	return formatDecimal(value, decimals, grouping, ""), nil
}

// formatDecimal renders f with fixed decimals, optional thousands grouping
// and an optional prefix/suffix style marker ("sym" prefixes, "code"
// suffixes).
func formatDecimal(f float64, decimals int, grouping bool, currency string) string {
	if decimals < 0 {
		if f != math.Trunc(f) {
			decimals = 2
		} else {
			decimals = 0
		}
	}
	neg := f < 0
	if neg {
		f = -f
	}
	body := strconv.FormatFloat(f, 'f', decimals, 64)
	if grouping {
		intPart := body
		frac := ""
		if dot := strings.IndexByte(body, '.'); dot >= 0 {
			intPart, frac = body[:dot], body[dot:]
		}
		var b strings.Builder
		for i, r := range intPart {
			if i > 0 && (len(intPart)-i)%3 == 0 {
				b.WriteByte(',')
			}
			b.WriteRune(r)
		}
		body = b.String() + frac
	}
	if currency != "" {
		body = currency + body
	}
	if neg {
		return "-" + body
	}
	return body
}

// currencySymbols covers common ISO 4217 codes; other codes are appended as
// the code itself ("1,234.56 CHF") to remain unambiguous.
var currencySymbols = map[string]string{
	"USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥", "CNY": "¥",
	"KRW": "₩", "INR": "₹", "RUB": "₽", "BRL": "R$", "CAD": "CA$",
	"AUD": "A$", "NZD": "NZ$", "SEK": "kr", "NOK": "kr", "DKK": "kr",
}

func fnFormatCurrency(args map[string]any, _ EvalContext) (any, error) {
	value := toNumber(args["value"])
	code, _ := args["currency"].(string)
	code = strings.ToUpper(strings.TrimSpace(code))
	decimals := -1
	if raw, ok := args["decimals"]; ok {
		decimals = int(toNumber(raw))
	}
	grouping := true
	if raw, ok := args["grouping"]; ok {
		grouping = toBoolean(raw)
	}
	if code == "" {
		return formatDecimal(value, decimals, grouping, ""), nil
	}
	if sym, ok := currencySymbols[code]; ok {
		return formatDecimal(value, decimals, grouping, sym), nil
	}
	return formatDecimal(value, decimals, grouping, "") + " " + code, nil
}

// fnFormatDate parses an ISO 8601 value (RFC 3339, date-only, or
// date+time-without-zone) and formats it with the supported TR35 subset.
// Unparseable values are returned as-is (progressive rendering); unknown
// pattern tokens pass through literally rather than failing.
func fnFormatDate(args map[string]any, _ EvalContext) (any, error) {
	value := Stringify(args["value"])
	format, _ := args["format"].(string)
	ts, ok := parseDate(value)
	if !ok {
		return value, nil
	}
	return formatTR35(ts, format), nil
}

var dateLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"15:04:05",
	"15:04",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range dateLayouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, true
		}
	}
	// Epoch seconds / milliseconds as bare numbers.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		sec, frac := math.Modf(f)
		if sec > 1e12 { // heuristically milliseconds
			sec /= 1000
			frac /= 1000
		}
		return time.Unix(int64(sec), int64(frac*1e9)).UTC(), true
	}
	return time.Time{}, false
}

// formatTR35 renders ts with the supported Unicode TR35 date-pattern subset:
// yy, yyyy (YYYY tolerated), M, MM, MMM, MMMM, d, dd (D/DD tolerated), E,
// EEEE, h, hh, H, HH, m, mm, s, ss and a. Unsupported token runs are copied
// verbatim so output degrades visibly but safely.
func formatTR35(ts time.Time, pattern string) string {
	var out strings.Builder
	runes := []rune(pattern)
	for i := 0; i < len(runes); {
		r := runes[i]
		if !unicode.IsLetter(r) {
			out.WriteRune(r)
			i++
			continue
		}
		j := i
		for j < len(runes) && runes[j] == r {
			j++
		}
		count := j - i
		out.WriteString(tr35Token(ts, r, count))
		i = j
	}
	return out.String()
}

func tr35Token(ts time.Time, r rune, count int) string {
	switch r {
	case 'y', 'Y':
		if count == 2 {
			// Two-digit years via modulo, so single-digit and negative years
			// (RFC 3339 allows "0009"; far-past epochs go below zero) render
			// zero-padded instead of slicing a too-short string.
			yy := ts.Year() % 100
			if yy < 0 {
				yy = -yy
			}
			return fmt.Sprintf("%02d", yy)
		}
		return strconv.Itoa(ts.Year())
	case 'M':
		switch count {
		case 1:
			return strconv.Itoa(int(ts.Month()))
		case 2:
			return fmt.Sprintf("%02d", int(ts.Month()))
		case 3:
			return ts.Month().String()[:3]
		default:
			return ts.Month().String()
		}
	case 'd', 'D':
		if count >= 2 {
			return fmt.Sprintf("%02d", ts.Day())
		}
		return strconv.Itoa(ts.Day())
	case 'E':
		if count >= 4 {
			return ts.Weekday().String()
		}
		return ts.Weekday().String()[:3]
	case 'h':
		h := ts.Hour() % 12
		if h == 0 {
			h = 12
		}
		if count >= 2 {
			return fmt.Sprintf("%02d", h)
		}
		return strconv.Itoa(h)
	case 'H':
		if count >= 2 {
			return fmt.Sprintf("%02d", ts.Hour())
		}
		return strconv.Itoa(ts.Hour())
	case 'm':
		if count >= 2 {
			return fmt.Sprintf("%02d", ts.Minute())
		}
		return strconv.Itoa(ts.Minute())
	case 's':
		if count >= 2 {
			return fmt.Sprintf("%02d", ts.Second())
		}
		return strconv.Itoa(ts.Second())
	case 'a':
		if ts.Hour() < 12 {
			return "AM"
		}
		return "PM"
	default:
		// Unsupported token (S, Z, z, w, ...): pass through verbatim.
		return strings.Repeat(string(r), count)
	}
}

// fnPluralize selects the CLDR plural-category string for value using real
// CLDR cardinal rules from x/text, falling back to "other".
func fnPluralize(args map[string]any, _ EvalContext) (any, error) {
	value := toNumber(args["value"])
	other, ok := args["other"].(string)
	if !ok {
		return nil, fmt.Errorf("a2ui: pluralize requires an 'other' fallback")
	}
	form := cardinalForm(value)
	names := map[plural.Form]string{
		plural.Zero: "zero", plural.One: "one", plural.Two: "two",
		plural.Few: "few", plural.Many: "many", plural.Other: "other",
	}
	if s, ok := args[names[form]].(string); ok {
		return s, nil
	}
	return other, nil
}

// cardinalForm computes the CLDR cardinal plural category for n (integers and
// simple fractions) under English rules via x/text's full CLDR tables.
func cardinalForm(n float64) plural.Form {
	intPart, frac := math.Modf(n)
	frac = math.Abs(frac)
	// f: visible fraction digits as an integer, v: count of fraction digits.
	f, v := 0, 0
	if frac > 0 {
		s := strconv.FormatFloat(frac, 'f', -1, 64)
		if dot := strings.IndexByte(s, '.'); dot >= 0 {
			digits := s[dot+1:]
			v = len(digits)
			if len(digits) > 9 {
				digits = digits[:9]
			}
			if parsed, err := strconv.Atoi(digits); err == nil {
				f = parsed
			}
		}
	}
	return plural.Cardinal.MatchPlural(language.English, int(intPart), v, v, f, f)
}

// fnOpenUrl validates the URL scheme (http/https only) and then reports
// ErrGated: actually opening must be confirmed by the user.
func fnOpenUrl(args map[string]any, _ EvalContext) (any, error) {
	raw := Stringify(args["url"])
	lower := strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return nil, fmt.Errorf("a2ui: openUrl refused non-http(s) URL %q", raw)
	}
	return nil, ErrGated
}

// fnAnd returns true when every value in the values array is truthy.
// Validation results count as their "valid" field.
func fnAnd(args map[string]any, _ EvalContext) (any, error) {
	values, _ := args["values"].([]any)
	for _, v := range values {
		if !toBoolean(v) {
			return false, nil
		}
	}
	return true, nil
}

// fnOr returns true when at least one value is truthy.
func fnOr(args map[string]any, _ EvalContext) (any, error) {
	values, _ := args["values"].([]any)
	for _, v := range values {
		if toBoolean(v) {
			return true, nil
		}
	}
	return false, nil
}

// fnNot negates a boolean value.
func fnNot(args map[string]any, _ EvalContext) (any, error) {
	return !toBoolean(args["value"]), nil
}

// CheckDisplayMessage picks the message to display for a failing check: the
// result's own message when present, else the rule's fallback message.
func CheckDisplayMessage(res ValidationResult, rule CheckRule) string {
	if m := res.Message(); m != "" {
		return m
	}
	return rule.Message
}

// EvalCheck evaluates a CheckRule condition to a ValidationResult. Both v1.0
// (condition yields a ValidationResult object) and v0.9.1 (condition yields a
// boolean) shapes are accepted; bare function calls with a sibling "message"
// (as used in the spec's own contact-form example) are tolerated too.
func EvalCheck(rule CheckRule, ctx EvalContext) ValidationResult {
	v, err := rule.Condition.Evaluate(ctx)
	if err != nil {
		return invalidMsg(fmt.Sprintf("check could not be evaluated: %v", err))
	}
	switch node := v.(type) {
	case ValidationResult:
		return node
	case map[string]any:
		res := ValidationResult(node)
		if _, ok := res["valid"]; ok {
			return res
		}
		return ValidationResult{"valid": len(node) > 0}
	case bool:
		if node {
			return valid()
		}
		return invalidMsg(rule.Message)
	case nil:
		return invalidMsg(rule.Message)
	default:
		return ValidationResult{"valid": false, "message": "check returned an unexpected type"}
	}
}
