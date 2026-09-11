// Package a2ui implements a from-scratch Go engine for the A2UI
// (Agent-to-UI) protocol, versions v0.9/v0.9.1 and v1.0. It decodes
// agent-to-renderer envelopes, maintains surfaces (component adjacency lists
// plus a JSON data model), materializes display trees, evaluates dynamic
// values (literals, data-model paths and catalog function calls) and builds
// renderer-to-agent messages and transport metadata.
//
// All inputs are treated as untrusted: every decoder is tolerant (unknown
// component names, versions and properties degrade gracefully instead of
// erroring) and hard size limits are enforced throughout.
package a2ui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/HenryNebula/a2a-tui/internal/a2ui/jsonptr"
)

// DynamicKind classifies how a dynamic value obtains its result.
type DynamicKind int

// Kinds of dynamic value.
const (
	// KindLiteral is a plain JSON value (string, number, boolean, array or a
	// literal object without path/call keys).
	KindLiteral DynamicKind = iota
	// KindPath is a {"path": "<json-pointer>"} data binding.
	KindPath
	// KindCall is a {"call": "<fn>", "args": {...}} function invocation.
	KindCall
)

// Call describes a catalog function invocation (common_types.json
// FunctionCall). Args are kept raw; each argument may itself be a dynamic
// value and is evaluated recursively at call time.
type Call struct {
	// Name is the function name ("@index" for the system index function).
	Name string `json:"call"`
	// CatalogID optionally overrides the surface's default catalog.
	CatalogID string `json:"catalogId,omitempty"`
	// Args holds the raw (possibly dynamic) arguments.
	Args map[string]any `json:"args,omitempty"`
	// ReturnType is a v0.9.1-only hint field; tolerated and ignored.
	ReturnType string `json:"returnType,omitempty"`
}

// Dynamic is a DynamicString/DynamicNumber/DynamicBoolean/DynamicStringList/
// DynamicValue per common_types.json: a literal, a {"path": ...} binding or a
// {"call": ...} function invocation. Dynamic implements json.Unmarshaler by
// sniffing those shapes; a zero Dynamic is an empty literal.
type Dynamic struct {
	Kind    DynamicKind
	Literal any
	Path    string
	Call    *Call
}

// UnmarshalJSON sniffs the wire shape of a dynamic value.
func (d *Dynamic) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	// Empty input (only reachable via direct calls; encoding/json rejects it
	// first) degrades to a literal nil, like the "null" case.
	if trimmed == "" || trimmed == "null" {
		d.Kind, d.Literal, d.Path, d.Call = KindLiteral, nil, "", nil
		return nil
	}
	if trimmed[0] != '{' {
		var lit any
		if err := json.Unmarshal(data, &lit); err != nil {
			return err
		}
		d.Kind, d.Literal = KindLiteral, lit
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if _, ok := probe["call"]; ok {
		var c Call
		if err := json.Unmarshal(data, &c); err != nil {
			return err
		}
		d.Kind, d.Call = KindCall, &c
		return nil
	}
	if raw, ok := probe["path"]; ok {
		var p string
		if err := json.Unmarshal(raw, &p); err != nil {
			return err
		}
		d.Kind, d.Path = KindPath, p
		return nil
	}
	// A literal object (DynamicValue permits objects without path/call).
	var lit any
	if err := json.Unmarshal(data, &lit); err != nil {
		return err
	}
	d.Kind, d.Literal = KindLiteral, lit
	return nil
}

// MarshalJSON re-encodes the dynamic value in wire form.
func (d Dynamic) MarshalJSON() ([]byte, error) {
	switch d.Kind {
	case KindPath:
		return json.Marshal(map[string]string{"path": d.Path})
	case KindCall:
		return json.Marshal(d.Call)
	default:
		return json.Marshal(d.Literal)
	}
}

// EvalContext carries everything needed to evaluate a Dynamic: the surface
// data model, the current collection-scope element and iteration index, and
// the function registry.
type EvalContext struct {
	// Root is the surface data model (absolute paths resolve against it).
	Root any
	// Scope is the current list-template element (relative paths resolve
	// against it; nil means root scope).
	Scope any
	// Index is the 0-based template iteration index, or nil outside templates.
	Index *int
	// Funcs resolves function calls; nil disables calls.
	Funcs *FunctionRegistry
	// depth guards nested expression evaluation.
	depth int
}

// child returns a copy one level deeper for nested evaluation.
func (c EvalContext) child() (EvalContext, bool) {
	if c.depth >= maxEvalDepth {
		return c, false
	}
	c.depth++
	return c, true
}

// maxEvalDepth bounds nested function-call/formatString evaluation.
const maxEvalDepth = 16

// Resolve resolves a data-model path. Absolute paths (leading '/') resolve
// against Root; relative paths resolve against Scope (Collection Scope per
// the spec's data-binding rules). Missing data returns (nil, false) — never
// an error, so progressive rendering keeps working.
func (c EvalContext) Resolve(path string) (any, bool) {
	if strings.HasPrefix(path, "/") {
		return jsonptr.Get(c.Root, path)
	}
	if c.Scope == nil {
		return nil, false
	}
	return jsonptr.Get(c.Scope, "/"+path)
}

// Evaluate resolves the dynamic value to a plain Go value. Errors are
// returned for malformed function calls (unknown function, bad arguments);
// missing paths evaluate to nil.
func (d Dynamic) Evaluate(ctx EvalContext) (any, error) {
	switch d.Kind {
	case KindLiteral:
		return evaluateValue(d.Literal, ctx)
	case KindPath:
		v, _ := ctx.Resolve(d.Path)
		return v, nil
	case KindCall:
		if d.Call == nil {
			return nil, nil
		}
		return evaluateCall(d.Call, ctx)
	default:
		return nil, nil
	}
}

// evaluateValue recursively evaluates dynamic markers embedded inside literal
// arrays and objects (e.g. nested calls inside `and.args.values`).
func evaluateValue(v any, ctx EvalContext) (any, error) {
	switch node := v.(type) {
	case map[string]any:
		if isDynamicMarker(node) {
			return evaluateMarker(node, ctx)
		}
		out := make(map[string]any, len(node))
		for k, item := range node {
			ev, err := evaluateValue(item, ctx)
			if err != nil {
				return nil, err
			}
			out[k] = ev
		}
		return out, nil
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			ev, err := evaluateValue(item, ctx)
			if err != nil {
				return nil, err
			}
			out[i] = ev
		}
		return out, nil
	default:
		return v, nil
	}
}

// isDynamicMarker reports whether m is exactly a {"path": string} or
// {"call": string, ...} wrapper. Maps that merely contain a "path" key among
// others are literal objects.
func isDynamicMarker(m map[string]any) bool {
	if len(m) == 1 {
		if _, ok := m["path"].(string); ok {
			return true
		}
	}
	if _, ok := m["call"]; ok {
		return true
	}
	return false
}

func evaluateMarker(m map[string]any, ctx EvalContext) (any, error) {
	var d Dynamic
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	if err := d.UnmarshalJSON(raw); err != nil {
		return nil, err
	}
	return d.Evaluate(ctx)
}

// evaluateCall evaluates a FunctionCall: @index from context, everything else
// through the registry after recursively evaluating arguments.
func evaluateCall(c *Call, ctx EvalContext) (any, error) {
	if c == nil {
		return nil, nil
	}
	childCtx, ok := ctx.child()
	if !ok {
		return nil, fmt.Errorf("a2ui: expression nesting deeper than %d", maxEvalDepth)
	}
	if c.Name == "@index" {
		if ctx.Index == nil {
			return nil, fmt.Errorf("a2ui: @index used outside a template scope")
		}
		offset := 0.0
		if rawOff, ok := c.Args["offset"]; ok {
			off, err := evaluateValue(rawOff, childCtx)
			if err != nil {
				return nil, err
			}
			offset = toNumber(off)
		}
		return float64(*ctx.Index) + offset, nil
	}
	args := make(map[string]any, len(c.Args))
	for k, raw := range c.Args {
		v, err := evaluateValue(raw, childCtx)
		if err != nil {
			return nil, fmt.Errorf("a2ui: argument %q of %s: %w", k, c.Name, err)
		}
		args[k] = v
	}
	if ctx.Funcs == nil {
		return nil, fmt.Errorf("a2ui: no function registry available for %s", c.Name)
	}
	fn, ok := ctx.Funcs.Lookup(c.Name)
	if !ok {
		return nil, fmt.Errorf("a2ui: unknown function %q", c.Name)
	}
	return fn(args, ctx)
}

// Stringify converts an evaluated value to its display string per the spec's
// type-conversion rules: numbers and booleans use standard representations,
// null becomes "", objects and arrays are compact JSON.
func Stringify(v any) string {
	switch node := v.(type) {
	case nil:
		return ""
	case string:
		return node
	case bool:
		if node {
			return "true"
		}
		return "false"
	case float64:
		return formatFloat(node)
	case json.Number:
		return node.String()
	case []any, map[string]any:
		b, err := json.Marshal(node)
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return fmt.Sprintf("%v", node)
	}
}

// formatFloat renders a JSON number without exponent noise.
func formatFloat(f float64) string {
	if f == float64(int64(f)) && f < 1e15 && f > -1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// toNumber coerces an evaluated value to a float64 (0 when not numeric).
func toNumber(v any) float64 {
	switch node := v.(type) {
	case float64:
		return node
	case float32:
		return float64(node)
	case int:
		return float64(node)
	case int64:
		return float64(node)
	case json.Number:
		f, _ := node.Float64()
		return f
	case bool:
		if node {
			return 1
		}
		return 0
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(node), 64)
		return f
	default:
		return 0
	}
}

// toBoolean coerces an evaluated value to a bool. Validation results
// ({"valid": bool}) count as booleans so `and`/`or`/`not` can compose with
// validators, as in the spec's button-validation example.
func toBoolean(v any) bool {
	switch node := v.(type) {
	case nil:
		return false
	case bool:
		return node
	case ValidationResult:
		return node.Valid()
	case map[string]any:
		if raw, ok := node["valid"]; ok {
			return toBoolean(raw)
		}
		return len(node) > 0
	case []any:
		return len(node) > 0
	case string:
		return node != ""
	case float64:
		return node != 0
	case int:
		return node != 0
	default:
		return true
	}
}

// EvalString evaluates d and coerces the result to a string, returning ""
// for missing paths and failed calls (progressive rendering: unresolved
// values render as empty rather than erroring).
func (d Dynamic) EvalString(ctx EvalContext) string {
	v, err := d.Evaluate(ctx)
	if err != nil {
		return ""
	}
	return Stringify(v)
}

// EvalNumber evaluates d and coerces the result to a float64 (0 on failure).
func (d Dynamic) EvalNumber(ctx EvalContext) float64 {
	v, err := d.Evaluate(ctx)
	if err != nil {
		return 0
	}
	return toNumber(v)
}

// EvalBoolean evaluates d and coerces the result to a bool (false on failure).
func (d Dynamic) EvalBoolean(ctx EvalContext) bool {
	v, err := d.Evaluate(ctx)
	if err != nil {
		return false
	}
	return toBoolean(v)
}

// EvalStringList evaluates d and coerces the result to a []string: literal
// string arrays pass through, single strings are wrapped, missing paths
// yield nil.
func (d Dynamic) EvalStringList(ctx EvalContext) []string {
	v, err := d.Evaluate(ctx)
	if err != nil {
		return nil
	}
	switch node := v.(type) {
	case []any:
		out := make([]string, 0, len(node))
		for _, item := range node {
			out = append(out, Stringify(item))
		}
		return out
	case string:
		return []string{node}
	default:
		return nil
	}
}
