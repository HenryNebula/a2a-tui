package a2ui

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestHostileDoesNotHang runs the nastiest graphs under a hard deadline:
// deep nesting, huge arrays, cycles and template bombs must all terminate
// quickly without panicking.
func TestHostileDoesNotHang(t *testing.T) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("panic under hostile input: %v", r)
			}
		}()

		// 1. Cycle plus deep chain.
		e := NewEngine()
		var b strings.Builder
		b.WriteString(`[{"version":"v1.0","createSurface":{"surfaceId":"h","components":[`)
		b.WriteString(`{"id":"root","component":"Column","children":["a","c0"]},`)
		b.WriteString(`{"id":"a","component":"Column","children":["root"]},`)
		for i := 0; i < 400; i++ {
			b.WriteString(`{"id":"c` + itoaStr(i) + `","component":"Column","children":["c` + itoaStr(i+1) + `"]},`)
		}
		b.WriteString(`{"id":"c400","component":"Text","text":"deep"}]}}]`)
		envs, err := DecodeEnvelopes([]byte(b.String()))
		if err != nil {
			t.Errorf("decode hostile chain: %v", err)
			return
		}
		_ = e.Apply(context.Background(), envs)
		if s := e.Surface("h"); s != nil {
			if _, err := s.Materialize(); err != nil {
				t.Errorf("materialize hostile chain: %v", err)
			}
		}

		// 2. Huge template array (200k elements).
		items := strings.Repeat(`{"x":1},`, 200000)
		var model any
		raw := `{"items":[` + items[:len(items)-1] + `]}`
		if err := json.Unmarshal([]byte(raw), &model); err != nil {
			t.Errorf("huge array decode: %v", err)
			return
		}
		s := NewSurface("bomb")
		s.DataModel = model
		var comps []Component
		_ = json.Unmarshal([]byte(`[
			{"id":"root","component":"List","children":{"path":"/items","componentId":"t"}},
			{"id":"t","component":"Text","text":"x"}
		]`), &comps)
		for i := range comps {
			s.Components[comps[i].ID] = &comps[i]
		}
		if _, err := s.Materialize(); err != nil {
			t.Errorf("materialize bomb: %v", err)
		}

		// 3. Deeply-nested pointer path into a scalar.
		if v, ok := (EvalContext{Root: "scalar"}).Resolve("/a/b/c"); ok {
			t.Errorf("pointer into scalar returned %v", v)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("hostile inputs did not terminate within 10s")
	}
}

// TestEngineConcurrency applies envelopes while reading surfaces from other
// goroutines (the engine must be safe for the TUI's snapshot path).
func TestEngineConcurrency(t *testing.T) {
	e := NewEngine()
	apply(t, e, `{"version":"v1.0","createSurface":{"surfaceId":"c"}}`)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(2)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				envs, _ := DecodeEnvelopes([]byte(`{"version":"v1.0","updateComponents":{"surfaceId":"c","components":[{"id":"t` + itoaStr(w) + `","component":"Text","text":"x"}]}}`))
				_ = e.Apply(context.Background(), envs)
			}
		}(w)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				if s := e.Surface("c"); s != nil {
					_, _ = s.Materialize()
				}
				_ = e.DataModelMetadata()
				_ = e.SurfaceIDs()
			}
		}()
	}
	wg.Wait()
}

func itoaStr(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
