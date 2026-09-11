package a2ui

import (
	"encoding/json"
	"testing"
)

func mustDynamic(t *testing.T, raw string) Dynamic {
	t.Helper()
	var d Dynamic
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return d
}

func testCtx() EvalContext {
	var root any
	_ = json.Unmarshal([]byte(`{
		"company": "Acme Corp",
		"users": [
			{"name": "Alice", "role": "Engineer", "n": 3},
			{"name": "Bob", "role": "Designer", "n": 1}
		],
		"empty": "",
		"flag": true,
		"tags": ["a", "b"]
	}`), &root)
	return EvalContext{Root: root, Funcs: NewFunctionRegistry()}
}

// TestDynamicMatrix covers literal / path / call shapes and evaluation.
func TestDynamicMatrix(t *testing.T) {
	ctx := testCtx()
	scoped := ctx
	i := 0
	scoped.Scope = map[string]any{"name": "InScope"}
	scoped.Index = &i

	cases := []struct {
		name string
		raw  string
		ctx  EvalContext
		want any
	}{
		{"string literal", `"hello"`, ctx, "hello"},
		{"number literal", `42.5`, ctx, 42.5},
		{"bool literal", `true`, ctx, true},
		{"null literal", `null`, ctx, nil},
		{"array literal", `["x","y"]`, ctx, []any{"x", "y"}},
		{"object literal", `{"a":{"b":1}}`, ctx, map[string]any{"a": map[string]any{"b": float64(1)}}},
		{"absolute path", `{"path":"/company"}`, ctx, "Acme Corp"},
		{"array index path", `{"path":"/users/1/name"}`, ctx, "Bob"},
		{"missing path", `{"path":"/nope"}`, ctx, nil},
		{"deep missing path", `{"path":"/nope/deeper"}`, ctx, nil},
		{"relative path in scope", `{"path":"name"}`, scoped, "InScope"},
		{"relative path without scope", `{"path":"name"}`, ctx, nil},
		{"absolute path in scope", `{"path":"/company"}`, scoped, "Acme Corp"},
		{"call literal", `{"call":"and","args":{"values":[true,true]}}`, ctx, true},
		{"call with path arg", `{"call":"not","args":{"value":{"path":"/flag"}}}`, ctx, false},
		{"call nested", `{"call":"or","args":{"values":[{"call":"not","args":{"value":true}},{"path":"/flag"}]}}`, ctx, true},
		{"formatString call", `{"call":"formatString","args":{"value":"Hi ${/users/0/name}"}}`, ctx, "Hi Alice"},
		{"index call", `{"call":"@index"}`, scoped, float64(0)},
		{"index call with offset", `{"call":"@index","args":{"offset":1}}`, scoped, float64(1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := mustDynamic(t, tc.raw)
			got, err := d.Evaluate(tc.ctx)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			switch want := tc.want.(type) {
			case []any:
				gotList, ok := got.([]any)
				if !ok || len(gotList) != len(want) {
					t.Fatalf("got %#v want %#v", got, tc.want)
				}
				for i := range want {
					if gotList[i] != want[i] {
						t.Fatalf("element %d: got %#v want %#v", i, gotList[i], want[i])
					}
				}
			case map[string]any:
				if _, ok := got.(map[string]any); !ok {
					t.Fatalf("got %#v want %#v", got, tc.want)
				}
			default:
				if got != tc.want {
					t.Fatalf("got %#v want %#v", got, tc.want)
				}
			}
		})
	}
}

// TestDynamicZeroValues pins the progressive-rendering accessors: unresolved
// or failed evaluations yield zero values, never errors out of the accessor.
func TestDynamicZeroValues(t *testing.T) {
	ctx := testCtx()
	if got := (Dynamic{Kind: KindPath, Path: "/missing"}).EvalString(ctx); got != "" {
		t.Errorf("missing path EvalString = %q, want empty", got)
	}
	if got := (Dynamic{Kind: KindPath, Path: "/missing"}).EvalNumber(ctx); got != 0 {
		t.Errorf("missing path EvalNumber = %v, want 0", got)
	}
	if got := (Dynamic{Kind: KindPath, Path: "/missing"}).EvalBoolean(ctx); got {
		t.Error("missing path EvalBoolean = true, want false")
	}
	if got := (Dynamic{Kind: KindPath, Path: "/missing"}).EvalStringList(ctx); got != nil {
		t.Errorf("missing path EvalStringList = %v, want nil", got)
	}
	// Unknown function: accessor swallows the error.
	d := mustDynamic(t, `{"call":"noSuchFn","args":{}}`)
	if got := d.EvalString(ctx); got != "" {
		t.Errorf("unknown fn EvalString = %q, want empty", got)
	}
	// Type coercions through accessors.
	num := mustDynamic(t, `{"path":"/users/0/n"}`)
	if got := num.EvalString(ctx); got != "3" {
		t.Errorf("number EvalString = %q, want 3", got)
	}
	str := mustDynamic(t, `"3.5"`)
	if got := str.EvalNumber(ctx); got != 3.5 {
		t.Errorf("string EvalNumber = %v, want 3.5", got)
	}
	boolish := mustDynamic(t, `{"valid":false}`)
	if got := boolish.EvalBoolean(ctx); got {
		t.Error("object with valid:false EvalBoolean = true, want false")
	}
	list := mustDynamic(t, `{"path":"/tags"}`)
	if got := list.EvalStringList(ctx); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("EvalStringList = %v", got)
	}
}

// TestIndexOutsideScope pins @index failing outside templates.
func TestIndexOutsideScope(t *testing.T) {
	ctx := testCtx()
	d := mustDynamic(t, `{"call":"@index"}`)
	if _, err := d.Evaluate(ctx); err == nil {
		t.Fatal("@index outside a template should error")
	}
}

// TestStringify pins the spec's interpolation type-conversion rules.
func TestStringify(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"x", "x"},
		{true, "true"},
		{false, "false"},
		{float64(42), "42"},
		{3.5, "3.5"},
		{[]any{"a"}, `["a"]`},
		{map[string]any{"k": "v"}, `{"k":"v"}`},
	}
	for _, tc := range cases {
		if got := Stringify(tc.in); got != tc.want {
			t.Errorf("Stringify(%#v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDynamicRoundTrip checks marshal/unmarshal stability.
func TestDynamicRoundTrip(t *testing.T) {
	for _, raw := range []string{`"s"`, `1.5`, `true`, `null`, `["a"]`, `{"a":1}`, `{"path":"/x"}`, `{"call":"and","args":{"values":[true,false]}}`} {
		d := mustDynamic(t, raw)
		out, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("marshal %s: %v", raw, err)
		}
		var back Dynamic
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("unmarshal %s: %v", out, err)
		}
		if back.Kind != d.Kind {
			t.Errorf("kind round trip %s -> %s -> kind %d", raw, out, back.Kind)
		}
	}
}

// TestDynamicUnmarshalEmpty guards UnmarshalJSON against empty and
// whitespace-only bytes: the exported method must degrade to a literal nil
// like the "null" case rather than panicking on trimmed[0]. (encoding/json
// itself rejects such input before reaching the unmarshaler, but the method
// is also called directly with raw sub-values.)
func TestDynamicUnmarshalEmpty(t *testing.T) {
	for _, raw := range []string{``, ` `, "\t\n\r "} {
		var d Dynamic
		if err := d.UnmarshalJSON([]byte(raw)); err != nil {
			t.Errorf("UnmarshalJSON(%q): %v", raw, err)
		}
		if d.Kind != KindLiteral || d.Literal != nil || d.Path != "" || d.Call != nil {
			t.Errorf("UnmarshalJSON(%q) = %+v, want zero literal-nil Dynamic", raw, d)
		}
	}
	// The standard-library entry point errors out before calling the
	// unmarshaler; it must never panic.
	var d Dynamic
	if err := json.Unmarshal([]byte("  "), &d); err == nil {
		t.Error(`json.Unmarshal("  ") should report a syntax error`)
	}
}

// TestEvalDepthGuard ensures nested expressions cannot recurse forever.
func TestEvalDepthGuard(t *testing.T) {
	ctx := testCtx()
	// Self-amplifying nested formatString.
	inner := map[string]any{"call": "formatString", "args": map[string]any{"value": "x"}}
	for i := 0; i < 40; i++ {
		inner = map[string]any{"call": "and", "args": map[string]any{"values": []any{inner, true}}}
	}
	raw, _ := json.Marshal(inner)
	var d Dynamic
	_ = d.UnmarshalJSON(raw)
	if _, err := d.Evaluate(ctx); err == nil {
		t.Fatal("deeply nested evaluation should hit the depth guard")
	}
}
