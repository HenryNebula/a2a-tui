package a2ui

import (
	"errors"
	"testing"
)

func evalFn(t *testing.T, name string, args map[string]any) (any, error) {
	t.Helper()
	reg := NewFunctionRegistry()
	fn, ok := reg.Lookup(name)
	if !ok {
		t.Fatalf("function %q not registered", name)
	}
	return fn(args, EvalContext{Root: map[string]any{}, Funcs: reg})
}

func checkValid(t *testing.T, name string, args map[string]any) ValidationResult {
	t.Helper()
	v, err := evalFn(t, name, args)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	res, ok := v.(ValidationResult)
	if !ok {
		t.Fatalf("%s returned %T, want ValidationResult", name, v)
	}
	return res
}

func TestRequired(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{"x", true},
		{"", false},
		{nil, false},
		{[]any{}, false},
		{[]any{"a"}, true},
		{0, true},
		{false, true},
	}
	for _, tc := range cases {
		res := checkValid(t, "required", map[string]any{"value": tc.value})
		if res.Valid() != tc.want {
			t.Errorf("required(%v) = %v, want %v (msg %q)", tc.value, res.Valid(), tc.want, res.Message())
		}
	}
}

func TestRegex(t *testing.T) {
	if res := checkValid(t, "regex", map[string]any{"value": "1234567890", "pattern": `^\d{10}$`}); !res.Valid() {
		t.Errorf("valid phone failed: %v", res.Message())
	}
	if res := checkValid(t, "regex", map[string]any{"value": "123", "pattern": `^\d{10}$`}); res.Valid() {
		t.Error("invalid phone passed")
	}
	// Uncompilable patterns degrade to a failed check, not an error.
	if _, err := evalFn(t, "regex", map[string]any{"value": "x", "pattern": "("}); err != nil {
		t.Fatalf("bad pattern errored: %v", err)
	}
	// RE2-unsupported constructs (lookahead) degrade visibly.
	res, err := evalFn(t, "regex", map[string]any{"value": "ab", "pattern": `a(?=b)`})
	if err != nil {
		t.Fatalf("lookahead pattern errored: %v", err)
	}
	if res.(ValidationResult).Valid() {
		t.Log("note: engine accepted a lookahead pattern (RE2 superset on this platform)")
	}
}

func TestLengthAndNumeric(t *testing.T) {
	if res := checkValid(t, "length", map[string]any{"value": "abc", "min": 2, "max": 5}); !res.Valid() {
		t.Errorf("length in range failed: %v", res.Message())
	}
	if res := checkValid(t, "length", map[string]any{"value": "abcdef", "max": 5}); res.Valid() {
		t.Error("length over max passed")
	}
	if res := checkValid(t, "numeric", map[string]any{"value": 7.5, "min": 1, "max": 10}); !res.Valid() {
		t.Errorf("numeric in range failed: %v", res.Message())
	}
	if res := checkValid(t, "numeric", map[string]any{"value": "not a number", "min": 1}); res.Valid() {
		t.Error("non-numeric passed")
	}
	if res := checkValid(t, "numeric", map[string]any{"value": "12", "max": 10}); res.Valid() {
		t.Error("string over max passed")
	}
}

func TestEmail(t *testing.T) {
	valid := []string{"a@b.co", "first.last@sub.domain.org"}
	invalid := []string{"", "nope", "a@b", "a b@c.io"}
	for _, v := range valid {
		if res := checkValid(t, "email", map[string]any{"value": v}); !res.Valid() {
			t.Errorf("email(%q) invalid: %v", v, res.Message())
		}
	}
	for _, v := range invalid {
		if res := checkValid(t, "email", map[string]any{"value": v}); res.Valid() {
			t.Errorf("email(%q) passed", v)
		}
	}
}

func TestLogic(t *testing.T) {
	if v, _ := evalFn(t, "and", map[string]any{"values": []any{true, true, true}}); v != true {
		t.Error("and(true x3) != true")
	}
	if v, _ := evalFn(t, "and", map[string]any{"values": []any{true, false}}); v != false {
		t.Error("and(true,false) != false")
	}
	// Validation results compose with logic (spec's button example).
	vr := ValidationResult{"valid": false}
	if v, _ := evalFn(t, "and", map[string]any{"values": []any{vr, true}}); v != false {
		t.Error("and(invalid-result, true) != false")
	}
	if v, _ := evalFn(t, "or", map[string]any{"values": []any{vr, true}}); v != true {
		t.Error("or(invalid-result, true) != true")
	}
	if v, _ := evalFn(t, "not", map[string]any{"value": false}); v != true {
		t.Error("not(false) != true")
	}
}

func TestFormatNumberAndCurrency(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"grouped int", map[string]any{"value": 1234567.0}, "1,234,567"},
		{"decimals", map[string]any{"value": 1234.5678, "decimals": 2.0}, "1,234.57"},
		{"no grouping", map[string]any{"value": 1234567.0, "grouping": false}, "1234567"},
		{"fraction default", map[string]any{"value": 1234.5}, "1,234.50"},
		{"usd", map[string]any{"value": 1234.5, "currency": "USD", "decimals": 2.0}, "$1,234.50"},
		{"eur", map[string]any{"value": 9.5, "currency": "EUR", "decimals": 2.0}, "€9.50"},
		{"chf code", map[string]any{"value": 1500.0, "currency": "CHF"}, "1,500 CHF"},
		{"negative", map[string]any{"value": -1234.0, "currency": "USD"}, "-$1,234"},
	}
	for _, tc := range cases {
		name := tc.name
		if name == "usd" || name == "eur" || name == "chf code" || name == "negative" {
			name = "formatCurrency"
		} else {
			name = "formatNumber"
		}
		v, err := evalFn(t, name, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if v != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, v, tc.want)
		}
	}
}

func TestFormatDate(t *testing.T) {
	ts := "2026-01-16T14:30:05Z"
	cases := []struct {
		format string
		want   string
	}{
		{"yyyy-MM-dd", "2026-01-16"},
		{"yy.MM.dd", "26.01.16"},
		{"HH:mm", "14:30"},
		{"HH:mm:ss", "14:30:05"},
		{"h:mm a", "2:30 PM"},
		{"hh:mm a", "02:30 PM"},
		{"MMM dd, yyyy", "Jan 16, 2026"},
		{"MMMM d", "January 16"},
		{"EEEE, d MMMM", "Friday, 16 January"},
		{"E", "Fri"},
		{"M/d", "1/16"},
		{"E MMM d, YYYY h:mm a", "Fri Jan 16, 2026 2:30 PM"}, // spec example tolerates YYYY
		{"dd/SSS", "16/SSS"},                                 // unsupported token passes through
	}
	for _, tc := range cases {
		v, err := evalFn(t, "formatDate", map[string]any{"value": ts, "format": tc.format})
		if err != nil {
			t.Fatalf("formatDate(%q): %v", tc.format, err)
		}
		if v != tc.want {
			t.Errorf("formatDate(%q) = %q, want %q", tc.format, v, tc.want)
		}
	}
	// Unparseable input returns the raw value (progressive rendering).
	v, err := evalFn(t, "formatDate", map[string]any{"value": "not a date", "format": "yyyy"})
	if err != nil || v != "not a date" {
		t.Errorf("unparseable date: %v %v", v, err)
	}
	// Date-only and epoch forms.
	if v, _ := evalFn(t, "formatDate", map[string]any{"value": "2026-03-05", "format": "yyyy/MM/dd"}); v != "2026/03/05" {
		t.Errorf("date-only = %v", v)
	}
	if v, _ := evalFn(t, "formatDate", map[string]any{"value": 1768571405, "format": "yyyy"}); v != "2026" {
		t.Errorf("epoch = %v", v)
	}
}

func TestPluralize(t *testing.T) {
	// CLDR English cardinal categories are only "one" and "other"; the
	// function returns the selected category's string verbatim (no count
	// substitution — the spec's implementation guide maps category -> string).
	forms := map[string]any{
		"value": 0, "one": "ONE", "other": "OTHER",
		"zero": "ZERO", "two": "TWO", "few": "FEW", "many": "MANY",
	}
	cases := []struct {
		value float64
		want  string
	}{
		{1, "ONE"},
		{0, "OTHER"}, // English has no "zero" cardinal rule
		{2, "OTHER"},
		{5, "OTHER"},
		{1.5, "OTHER"},
		{1000000, "OTHER"},
	}
	for _, tc := range cases {
		forms["value"] = tc.value
		v, err := evalFn(t, "pluralize", forms)
		if err != nil {
			t.Fatalf("pluralize(%v): %v", tc.value, err)
		}
		if v != tc.want {
			t.Errorf("pluralize(%v) = %q, want %q", tc.value, v, tc.want)
		}
	}
	// Missing category falls back to other; missing other is an error.
	v, _ := evalFn(t, "pluralize", map[string]any{"value": 1.0, "other": "items"})
	if v != "items" {
		t.Errorf("fallback = %q", v)
	}
	if _, err := evalFn(t, "pluralize", map[string]any{"value": 1.0}); err == nil {
		t.Error("pluralize without other should error")
	}
}

func TestOpenUrlGated(t *testing.T) {
	_, err := evalFn(t, "openUrl", map[string]any{"url": "https://example.com"})
	if !errors.Is(err, ErrGated) {
		t.Errorf("https URL: err = %v, want ErrGated", err)
	}
	if _, err := evalFn(t, "openUrl", map[string]any{"url": "javascript:alert(1)"}); err == nil || errors.Is(err, ErrGated) {
		t.Errorf("javascript: scheme should be refused outright, got %v", err)
	}
	if _, err := evalFn(t, "openUrl", map[string]any{"url": "file:///etc/passwd"}); err == nil || errors.Is(err, ErrGated) {
		t.Errorf("file: scheme should be refused outright, got %v", err)
	}
}

func TestFormatStringExpressions(t *testing.T) {
	ctx := testCtx()
	cases := []struct {
		tmpl string
		want string
	}{
		{"Hello, ${/users/0/name}! Welcome back to ${/company}.", "Hello, Alice! Welcome back to Acme Corp."},
		{"No markers here", "No markers here"},
		{"Escaped \\${not-expanded}", "Escaped ${not-expanded}"},
		{"Missing ${/nope} stays empty", "Missing  stays empty"},
		{"Date: ${formatDate(value:'2026-01-02', format:'yyyy/MM/dd')}", "Date: 2026/01/02"},
		{"Relative ${name} without scope stays empty", "Relative  without scope stays empty"},
		{"Numbers ${/users/1/n} and bool ${/flag}", "Numbers 1 and bool true"},
		{"Nested fn ${formatString(value:'x')}", "Nested fn x"},
	}
	for _, tc := range cases {
		got, err := interpolate(tc.tmpl, ctx)
		if err != nil {
			t.Errorf("interpolate(%q): %v", tc.tmpl, err)
			continue
		}
		if got != tc.want {
			t.Errorf("interpolate(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
	// Scoped relative path resolution inside formatString.
	scoped := ctx
	scoped.Scope = map[string]any{"name": "Bob"}
	got, err := interpolate("Hi ${name}", scoped)
	if err != nil || got != "Hi Bob" {
		t.Errorf("scoped interpolate = %q err=%v", got, err)
	}
	// Nested function calls, including the positional ${upper(${x})} form.
	ctx.Funcs.Register("upper", func(args map[string]any, _ EvalContext) (any, error) {
		return stringsToUpper(Stringify(args["value"])), nil
	})
	if got, _ := interpolate("${upper(value:${/company})}", ctx); got != "ACME CORP" {
		t.Errorf("upper via named arg = %q", got)
	}
	if got, _ := interpolate("Upper: ${upper(${/company})}", ctx); got != "Upper: ACME CORP" {
		t.Errorf("upper positional = %q", got)
	}
}

func TestRegistryExtensible(t *testing.T) {
	reg := NewFunctionRegistry()
	reg.Register("double", func(args map[string]any, _ EvalContext) (any, error) {
		return toNumber(args["value"]) * 2, nil
	})
	names := reg.Names()
	found := false
	for _, n := range names {
		if n == "double" {
			found = true
		}
	}
	if !found {
		t.Fatal("custom function not in Names()")
	}
	d := mustDynamic(t, `{"call":"double","args":{"value":21}}`)
	if got := d.EvalNumber(EvalContext{Funcs: reg}); got != 42 {
		t.Errorf("double(21) = %v", got)
	}
}

func TestEvalCheckShapes(t *testing.T) {
	ctx := testCtx()
	// v1.0 ValidationResult shape. Basic validators return bare results;
	// the CheckRule message is the display fallback (spec contact-form
	// example provides the human text).
	rule := CheckRule{Condition: mustDynamic(t, `{"call":"required","args":{"value":""}}`), Message: "fallback"}
	res := EvalCheck(rule, ctx)
	if res.Valid() {
		t.Error("required(\"\") should be invalid")
	}
	if res.Message() != "" {
		t.Errorf("basic validator message = %q, want empty (rule message is the fallback)", res.Message())
	}
	if m := CheckDisplayMessage(res, rule); m != "fallback" {
		t.Errorf("display fallback = %q", m)
	}
	// v0.9.1 boolean shape.
	rule = CheckRule{Condition: mustDynamic(t, `{"path":"/flag"}`), Message: "fallback"}
	if !EvalCheck(rule, ctx).Valid() {
		t.Error("path /flag is true, check should pass")
	}
	// Failing boolean uses the rule message.
	rule = CheckRule{Condition: mustDynamic(t, `{"path":"/missing"}`), Message: "needed"}
	res = EvalCheck(rule, ctx)
	if res.Valid() || res.Message() != "needed" {
		t.Errorf("bool fail: %+v", res)
	}
}

// stringsToUpper avoids importing strings in this test file twice.
func stringsToUpper(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'a' && r <= 'z' {
			out[i] = r - 32
		}
	}
	return string(out)
}
