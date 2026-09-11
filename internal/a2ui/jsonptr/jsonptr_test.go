package jsonptr

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// rfcDoc is the example document from RFC 6901 section 5.
const rfcDoc = `{
  "foo": ["bar", "baz"],
  "": 0,
  "a/b": 1,
  "c%d": 2,
  "e^f": 3,
  "g|h": 4,
  "i\\j": 5,
  "k\"l": 6,
  " ": 7,
  "m~n": 8
}`

func mustDoc(t *testing.T) map[string]any {
	t.Helper()
	var doc any
	if err := json.Unmarshal([]byte(rfcDoc), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return doc.(map[string]any)
}

// TestGetRFC6901 runs the evaluation table from RFC 6901 section 5.
func TestGetRFC6901(t *testing.T) {
	doc := mustDoc(t)
	cases := []struct {
		ptr  string
		want any
	}{
		{"", doc},
		{"/foo", []any{"bar", "baz"}},
		{"/foo/0", "bar"},
		{"/", float64(0)},
		{"/a~1b", float64(1)},
		{"/c%d", float64(2)},
		{"/e^f", float64(3)},
		{"/g|h", float64(4)},
		{"/i\\j", float64(5)},
		{"/k\"l", float64(6)},
		{"/ ", float64(7)},
		{"/m~0n", float64(8)},
	}
	for _, tc := range cases {
		got, ok := Get(doc, tc.ptr)
		if !ok {
			t.Errorf("Get(%q): not found, want %#v", tc.ptr, tc.want)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Get(%q) = %#v, want %#v", tc.ptr, got, tc.want)
		}
	}
}

func TestGetMissing(t *testing.T) {
	doc := mustDoc(t)
	for _, ptr := range []string{"/foo/2", "/foo/-", "/foo/bar", "/nope", "/nope/deeper/still", "foo", "/a/b"} {
		if got, ok := Get(doc, ptr); ok {
			t.Errorf("Get(%q) = %v, want not-found", ptr, got)
		}
	}
}

func TestSetUpsert(t *testing.T) {
	doc := any(map[string]any{"a": map[string]any{"b": float64(1)}})
	root, err := Set(doc, "/a/b", float64(2))
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := Get(root, "/a/b"); got != float64(2) {
		t.Fatalf("/a/b = %v, want 2", got)
	}
	// Original snapshot is untouched (spine copy).
	if got, _ := Get(doc, "/a/b"); got != float64(1) {
		t.Fatalf("original mutated: /a/b = %v", got)
	}
	// Create intermediate objects.
	root, err = Set(root, "/x/y/z", "deep")
	if err != nil {
		t.Fatalf("Set deep: %v", err)
	}
	if got, ok := Get(root, "/x/y/z"); !ok || got != "deep" {
		t.Fatalf("/x/y/z = %v ok=%v", got, ok)
	}
	// Root replacement via the empty pointer.
	root, err = Set(root, "", "whole")
	if err != nil {
		t.Fatalf("Set root: %v", err)
	}
	if root != "whole" {
		t.Fatalf("root = %v", root)
	}
}

func TestSetArrays(t *testing.T) {
	doc := any([]any{map[string]any{"k": "v0"}, map[string]any{"k": "v1"}})
	root, err := Set(doc, "/0/k", "changed")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, _ := Get(root, "/0/k"); got != "changed" {
		t.Fatalf("/0/k = %v", got)
	}
	// Append one past the end.
	root, err = Set(doc, "/2", map[string]any{"k": "v2"})
	if err != nil {
		t.Fatalf("Set append: %v", err)
	}
	arr := root.([]any)
	if len(arr) != 3 {
		t.Fatalf("len = %d, want 3", len(arr))
	}
	// Beyond one-past-the-end is rejected.
	if _, err := Set(doc, "/5", 1); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("Set past end: err = %v, want ErrIndexOutOfRange", err)
	}
	// Numeric-looking object keys must not be treated as indices on maps.
	mdoc := any(map[string]any{"0": "zero"})
	root, err = Set(mdoc, "/0", "zero2")
	if err != nil {
		t.Fatalf("Set numeric key: %v", err)
	}
	if got, _ := Get(root, "/0"); got != "zero2" {
		t.Fatalf("/0 = %v", got)
	}
}

// TestSetArrayElementDirect pins Set whose final token is an existing array
// index: the element itself is replaced in place (recursing with an empty
// token list would panic on tokens[0]).
func TestSetArrayElementDirect(t *testing.T) {
	// Root-level array.
	arr := any([]any{"a", "b"})
	root, err := Set(arr, "/0", "changed")
	if err != nil {
		t.Fatalf("Set /0: %v", err)
	}
	if !reflect.DeepEqual(root.([]any), []any{"changed", "b"}) {
		t.Fatalf("Set /0 = %v, want [changed b]", root)
	}
	// Nested under an object key, non-scalar replacement value.
	doc := any(map[string]any{"items": []any{float64(1), float64(2)}})
	root, err = Set(doc, "/items/1", map[string]any{"k": "v"})
	if err != nil {
		t.Fatalf("Set /items/1: %v", err)
	}
	if got, _ := Get(root, "/items/1"); !reflect.DeepEqual(got, map[string]any{"k": "v"}) {
		t.Fatalf("/items/1 = %#v", got)
	}
	if got, _ := Get(root, "/items/0"); got != float64(1) {
		t.Fatalf("sibling /items/0 = %v, want 1", got)
	}
	if l := len(root.(map[string]any)["items"].([]any)); l != 2 {
		t.Fatalf("array length = %d, want 2", l)
	}
	// Original snapshot is untouched (spine copy).
	if got, _ := Get(doc, "/items/1"); got != float64(2) {
		t.Fatalf("original mutated: /items/1 = %v", got)
	}
	// Out-of-range replacement keeps the error semantics.
	if _, err := Set(doc, "/items/5", 1); !errors.Is(err, ErrIndexOutOfRange) {
		t.Fatalf("Set past end: err = %v, want ErrIndexOutOfRange", err)
	}
}

// TestDeleteNestedInsideArray pins Delete of a path inside an array element:
// the nested key is removed from the element, which stays in the array.
func TestDeleteNestedInsideArray(t *testing.T) {
	doc := any(map[string]any{
		"users": []any{
			map[string]any{"name": "Alice", "email": "a@example.com"},
			map[string]any{"name": "Bob", "email": "b@example.com"},
		},
	})
	root, ok, err := Delete(doc, "/users/0/email")
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	if got, _ := Get(root, "/users/0/email"); got != nil {
		t.Fatalf("/users/0/email = %v, want removed", got)
	}
	if got, _ := Get(root, "/users/0/name"); got != "Alice" {
		t.Fatalf("/users/0/name = %v (element must remain)", got)
	}
	if got, _ := Get(root, "/users/1/email"); got != "b@example.com" {
		t.Fatalf("/users/1/email = %v (sibling must be untouched)", got)
	}
	if l := len(root.(map[string]any)["users"].([]any)); l != 2 {
		t.Fatalf("users length = %d, want 2", l)
	}
	// Original snapshot is untouched (spine copy).
	if got, _ := Get(doc, "/users/0/email"); got != "a@example.com" {
		t.Fatalf("original mutated: %v", got)
	}
	// Deleting the whole element itself still splices.
	root, ok, err = Delete(doc, "/users/1")
	if err != nil || !ok {
		t.Fatalf("Delete element: ok=%v err=%v", ok, err)
	}
	if l := len(root.(map[string]any)["users"].([]any)); l != 1 {
		t.Fatalf("users length after splice = %d, want 1", l)
	}
	if got, _ := Get(root, "/users/0/name"); got != "Alice" {
		t.Fatalf("/users/0/name = %v after splice", got)
	}
}

func TestDelete(t *testing.T) {
	doc := mustDoc(t)
	root, ok, err := Delete(doc, "/a~1b")
	if err != nil || !ok {
		t.Fatalf("Delete: ok=%v err=%v", ok, err)
	}
	if _, exists := Get(root, "/a~1b"); exists {
		t.Fatal("key still present after delete")
	}
	// Missing key: no-op, root unchanged, ok=false.
	root2, ok, err := Delete(root, "/missing")
	if err != nil || ok || root2 == nil {
		t.Fatalf("Delete missing: ok=%v err=%v", ok, err)
	}
	// Array element removal splices.
	arrDoc := any([]any{"a", "b", "c"})
	root3, ok, err := Delete(arrDoc, "/1")
	if err != nil || !ok {
		t.Fatalf("Delete array: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(root3.([]any), []any{"a", "c"}) {
		t.Fatalf("array after delete = %v", root3)
	}
	// Deleting the whole document.
	_, ok, err = Delete(doc, "")
	if err != nil || !ok {
		t.Fatalf("Delete root: ok=%v err=%v", ok, err)
	}
}

func TestHostileLimits(t *testing.T) {
	deep := ""
	for i := 0; i < MaxTokens+1; i++ {
		deep += "/k"
	}
	if _, err := Set(any(map[string]any{}), deep, 1); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("deep set err = %v, want ErrTooDeep", err)
	}
	if _, _, err := Delete(any(map[string]any{}), deep); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("deep delete err = %v, want ErrTooDeep", err)
	}
	// "-" is rejected as an array token.
	arr := any([]any{1.0})
	if _, ok := Get(arr, "/-"); ok {
		t.Fatal(`Get("/-") should miss`)
	}
	if _, err := Set(arr, "/-", 2.0); err == nil {
		t.Fatal(`Set("/-") should error`)
	}
	// Non-canonical array index is a miss, not a panic.
	if _, ok := Get(arr, "/01"); ok {
		t.Fatal(`Get("/01") should miss`)
	}
	// Traversing a scalar yields a miss.
	if _, ok := Get(any("scalar"), "/x"); ok {
		t.Fatal("Get into scalar should miss")
	}
}
