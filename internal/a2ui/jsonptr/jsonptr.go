// Package jsonptr implements RFC 6901 JSON Pointer get, set and delete over
// trees of plain Go values (map[string]any, []any, string, float64, bool and
// nil) as produced by encoding/json unmarshalling into any.
//
// The implementation is deliberately small and hardened: pointer token counts
// and traversal depth are capped so that untrusted documents cannot cause
// excessive work, and the pointer grammar is validated strictly (array indices
// must be canonical decimals; "-" is rejected because A2UI never appends).
package jsonptr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Hard limits applied to untrusted pointers and documents.
const (
	// MaxTokens is the maximum number of reference tokens in one pointer.
	MaxTokens = 64
	// MaxDepth is the maximum document depth traversed while resolving.
	MaxDepth = 32
)

// Errors returned by this package.
var (
	// ErrInvalidPointer is returned for pointers that are not valid RFC 6901
	// pointers (e.g. a token used as an array index that is not a canonical
	// decimal, or "-" which A2UI never uses).
	ErrInvalidPointer = errors.New("jsonptr: invalid JSON Pointer")
	// ErrTooDeep is returned when a pointer exceeds MaxTokens tokens or the
	// document exceeds MaxDepth along the traversed path.
	ErrTooDeep = errors.New("jsonptr: pointer or document exceeds depth limits")
	// ErrIndexOutOfRange is returned by Set when an array index points past
	// the end of an array (one past the end appends, per upsert semantics).
	ErrIndexOutOfRange = errors.New("jsonptr: array index out of range")
	// ErrTypeMismatch is returned when a container of the wrong kind is found
	// along the path (e.g. an object token applied to a []any).
	ErrTypeMismatch = errors.New("jsonptr: path traverses a value of the wrong type")
)

// parse splits ptr into unescaped reference tokens. The empty pointer yields
// zero tokens (the whole document).
func parse(ptr string) ([]string, error) {
	if ptr == "" {
		return nil, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("%w: %q does not start with '/'", ErrInvalidPointer, ptr)
	}
	parts := strings.Split(ptr[1:], "/")
	if len(parts) > MaxTokens {
		return nil, fmt.Errorf("%w: %d tokens exceeds %d", ErrTooDeep, len(parts), MaxTokens)
	}
	for i, p := range parts {
		parts[i] = unescape(p)
	}
	return parts, nil
}

// unescape applies RFC 6901 ~1 ('/') and ~0 ('~') transformations.
func unescape(tok string) string {
	if !strings.Contains(tok, "~") {
		return tok
	}
	tok = strings.ReplaceAll(tok, "~1", "/")
	return strings.ReplaceAll(tok, "~0", "~")
}

// arrayIndex reports the array index encoded by tok, or an error when tok is
// not a canonical decimal index. "-" is explicitly rejected: A2UI performs
// whole-value replacement, never append.
func arrayIndex(tok string) (int, error) {
	if tok == "" {
		return 0, fmt.Errorf("%w: empty array index", ErrInvalidPointer)
	}
	if len(tok) > 1 && tok[0] == '0' {
		return 0, fmt.Errorf("%w: array index %q has a leading zero", ErrInvalidPointer, tok)
	}
	for i := 0; i < len(tok); i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return 0, fmt.Errorf("%w: %q is not an array index", ErrInvalidPointer, tok)
		}
	}
	n, err := strconv.Atoi(tok)
	if err != nil {
		return 0, fmt.Errorf("%w: array index %q: %v", ErrInvalidPointer, tok, err)
	}
	return n, nil
}

// Get resolves ptr in doc. It returns (value, true) when the pointer exists
// and (nil, false) otherwise. Errors are reserved for malformed pointers;
// missing data is not an error (progressive rendering relies on this).
func Get(doc any, ptr string) (any, bool) {
	tokens, err := parse(ptr)
	if err != nil {
		return nil, false
	}
	cur := doc
	for depth, tok := range tokens {
		if depth >= MaxDepth {
			return nil, false
		}
		switch node := cur.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return nil, false
			}
			cur = v
		case []any:
			idx, err := arrayIndex(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return nil, false
			}
			cur = node[idx]
		default:
			return nil, false
		}
	}
	return cur, true
}

// Set replaces or creates the value at ptr, creating intermediate objects as
// needed (A2UI updateDataModel upsert semantics). It returns the new root,
// which differs from doc only when ptr is the empty pointer. The path spine is
// copied so previously shared snapshots remain stable.
func Set(doc any, ptr string, val any) (any, error) {
	tokens, err := parse(ptr)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return val, nil
	}
	return setAt(doc, tokens, val, 0)
}

func setAt(cur any, tokens []string, val any, depth int) (any, error) {
	if depth >= MaxDepth {
		return nil, fmt.Errorf("%w: set path deeper than %d", ErrTooDeep, MaxDepth)
	}
	tok := tokens[0]
	rest := tokens[1:]
	if isIndex, idx := indexIfArray(cur, tok); isIndex {
		arr := cur.([]any)
		if idx == len(arr) {
			// Append at the end (upsert), then continue inside the new tail.
			child := any(nil)
			var err error
			if len(rest) > 0 {
				child, err = setAt(nil, rest, val, depth+1)
				if err != nil {
					return nil, err
				}
			} else {
				child = val
			}
			out := make([]any, len(arr)+1)
			copy(out, arr)
			out[len(arr)] = child
			return out, nil
		}
		if idx < 0 || idx > len(arr) {
			return nil, fmt.Errorf("%w: index %d in array of %d", ErrIndexOutOfRange, idx, len(arr))
		}
		if len(rest) == 0 {
			// The pointer targets the element itself: replace it in place
			// rather than recursing (an empty token list has no tokens[0]).
			out := make([]any, len(arr))
			copy(out, arr)
			out[idx] = val
			return out, nil
		}
		child, err := setAt(arr[idx], rest, val, depth+1)
		if err != nil {
			return nil, err
		}
		out := make([]any, len(arr))
		copy(out, arr)
		out[idx] = child
		return out, nil
	}
	if cur != nil {
		if _, ok := cur.(map[string]any); !ok {
			return nil, fmt.Errorf("%w: token %q applied to %T", ErrTypeMismatch, tok, cur)
		}
	}
	obj, _ := cur.(map[string]any)
	out := make(map[string]any, len(obj)+1)
	for k, v := range obj {
		out[k] = v
	}
	child := any(nil)
	var err error
	if len(rest) > 0 {
		existing, _ := obj[tok]
		child, err = setAt(existing, rest, val, depth+1)
		if err != nil {
			return nil, err
		}
	} else {
		child = val
	}
	if child == nil {
		// Setting null deletes the key, mirroring updateDataModel semantics.
		delete(out, tok)
	} else {
		out[tok] = child
	}
	return out, nil
}

// indexIfArray reports whether tok addresses cur as an array and, if so, the
// index it denotes. Non-canonical indices fall back to object-key handling so
// that maps with numeric-looking keys keep working.
func indexIfArray(cur any, tok string) (bool, int) {
	if _, ok := cur.([]any); !ok {
		return false, 0
	}
	idx, err := arrayIndex(tok)
	if err != nil {
		return false, 0
	}
	return true, idx
}

// Delete removes the value at ptr and returns the new root, whether a value
// was removed, and an error for malformed pointers.
func Delete(doc any, ptr string) (any, bool, error) {
	tokens, err := parse(ptr)
	if err != nil {
		return nil, false, err
	}
	if len(tokens) == 0 {
		return nil, doc != nil, nil
	}
	exists, err := pathExists(doc, tokens)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return doc, false, nil
	}
	return deleteAt(doc, tokens, 0)
}

func pathExists(doc any, tokens []string) (bool, error) {
	for depth, tok := range tokens {
		if depth >= MaxDepth {
			return false, fmt.Errorf("%w: path deeper than %d", ErrTooDeep, MaxDepth)
		}
		switch node := doc.(type) {
		case map[string]any:
			v, ok := node[tok]
			if !ok {
				return false, nil
			}
			doc = v
		case []any:
			idx, err := arrayIndex(tok)
			if err != nil || idx < 0 || idx >= len(node) {
				return false, nil
			}
			doc = node[idx]
		default:
			return false, nil
		}
	}
	return true, nil
}

func deleteAt(cur any, tokens []string, depth int) (any, bool, error) {
	if depth >= MaxDepth {
		return nil, false, fmt.Errorf("%w: delete path deeper than %d", ErrTooDeep, MaxDepth)
	}
	tok, rest := tokens[0], tokens[1:]
	if isIndex, idx := indexIfArray(cur, tok); isIndex {
		arr := cur.([]any)
		if idx < 0 || idx >= len(arr) {
			return nil, false, nil
		}
		if len(rest) == 0 {
			out := make([]any, 0, len(arr)-1)
			out = append(out, arr[:idx]...)
			out = append(out, arr[idx+1:]...)
			return out, true, nil
		}
		// Recurse into the element and rebuild the array copy around it.
		child, had, err := deleteAt(arr[idx], rest, depth+1)
		if err != nil {
			return nil, false, err
		}
		if !had {
			return arr, false, nil
		}
		out := make([]any, len(arr))
		copy(out, arr)
		out[idx] = child
		return out, true, nil
	}
	obj, ok := cur.(map[string]any)
	if !ok {
		return nil, false, fmt.Errorf("%w: delete token %q applied to %T", ErrTypeMismatch, tok, cur)
	}
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		out[k] = v
	}
	if len(rest) == 0 {
		_, had := out[tok]
		delete(out, tok)
		return out, had, nil
	}
	child, had, err := deleteAt(out[tok], rest, depth+1)
	if err != nil {
		return nil, false, err
	}
	if !had {
		return out, false, nil
	}
	out[tok] = child
	return out, true, nil
}
