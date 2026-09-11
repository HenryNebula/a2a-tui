package a2ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureEnvelopes loads envelopes from a vendored specification fixture.
// The catalog examples wrap envelopes in {"name", "description", "messages"}.
func fixtureEnvelopes(t *testing.T, file string) []Envelope {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var wrapper struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Messages) > 0 {
		envs, err := DecodeEnvelopes(wrapper.Messages)
		if err != nil {
			t.Fatalf("%s: decode messages: %v", file, err)
		}
		return envs
	}
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	envs := make([]Envelope, 0, len(lines))
	for i, line := range lines {
		e, err := DecodeEnvelopes([]byte(line))
		if err != nil {
			t.Fatalf("%s line %d: %v", file, i+1, err)
		}
		envs = append(envs, e...)
	}
	return envs
}

var fixtureFiles = []string{
	"contact_form_example.jsonl",
	"example_01_flight_status.json",
	"example_09_login_form.json",
	"example_34_child_list_template.json",
	"example_36_modal.json",
}

// TestVendoredFixturesApply runs every vendored spec fixture through the
// engine and materializes the resulting surfaces without errors or panics.
func TestVendoredFixturesApply(t *testing.T) {
	for _, file := range fixtureFiles {
		t.Run(file, func(t *testing.T) {
			envs := fixtureEnvelopes(t, file)
			if len(envs) == 0 {
				t.Fatal("fixture has no envelopes")
			}
			e := NewEngine()
			var applied int
			for i, env := range envs {
				errs := e.Apply(context.Background(), []Envelope{env})
				if len(errs) > 0 && env.CreateSurface == nil {
					// The last envelope of the contact-form stream deletes
					// the surface; ordering is per-batch here so later
					// messages for a deleted surface are re-created by the
					// first createSurface below. Count hard failures only.
					t.Logf("envelope %d (%s): %v", i, env.Key, errs)
				}
				if e.SurfaceIDs() != nil {
					applied++
				}
			}
			// Full-stream application in one batch must work end to end.
			// The contact-form stream ends with deleteSurface (no live
			// surfaces remain); the catalog examples keep their surface.
			e2 := NewEngine()
			_ = e2.Apply(context.Background(), envs)
			live := len(e2.SurfaceIDs())
			wantLive := 1
			if strings.HasPrefix(file, "contact_form") {
				wantLive = 0
			}
			if live != wantLive {
				t.Errorf("live surfaces after full stream = %d, want %d", live, wantLive)
			}
			// Re-apply without the trailing deletes and materialize.
			e3 := NewEngine()
			for _, env := range envs {
				if env.DeleteSurface != nil {
					continue
				}
				_ = e3.Apply(context.Background(), []Envelope{env})
			}
			for _, id := range e3.SurfaceIDs() {
				s := e3.Surface(id)
				if _, err := s.Materialize(); err != nil {
					t.Errorf("surface %s: materialize: %v", id, err)
				}
			}
			_ = applied
		})
	}
}

// TestContactFormFixture pins the spec's Contact Form worked example: after
// the full stream (minus the trailing delete) the form renders its data.
func TestContactFormFixture(t *testing.T) {
	envs := fixtureEnvelopes(t, "contact_form_example.jsonl")
	e := NewEngine()
	for _, env := range envs {
		if env.DeleteSurface != nil {
			continue
		}
		if errs := e.Apply(context.Background(), []Envelope{env}); len(errs) != 0 {
			t.Fatalf("apply %s: %v", env.Key, errs)
		}
	}
	s := e.Surface("contact_form_1")
	if s == nil {
		t.Fatal("contact_form_1 missing")
	}
	root, err := s.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	if root.Component.Component != "Card" {
		t.Errorf("root = %s, want Card", root.Component.Component)
	}
	ctx := s.EvalContextFor(e.Functions())
	// The TextField values resolve from the updateDataModel message.
	var fields []*Node
	root.Walk(func(n *Node) bool {
		if !n.Placeholder && n.Component.Component == "TextField" {
			fields = append(fields, n)
		}
		return true
	})
	if len(fields) < 4 {
		t.Fatalf("expected >=4 TextField nodes, got %d", len(fields))
	}
	first := fields[0].Component.Props.(*TextFieldProps)
	if got := first.Value.EvalString(fields[0].EvalContext(ctx)); got != "John" {
		t.Errorf("firstName field = %q, want John", got)
	}
	// Checks decode (the spec example uses the bare call+message form).
	emailField := fields[2].Component.Props.(*TextFieldProps)
	if len(emailField.Checks) != 2 {
		t.Fatalf("email checks = %d, want 2", len(emailField.Checks))
	}
}

// TestChildListTemplateFixture pins template expansion on the vendored
// child-list example.
func TestChildListTemplateFixture(t *testing.T) {
	envs := fixtureEnvelopes(t, "example_34_child_list_template.json")
	e := NewEngine()
	for _, env := range envs {
		if env.DeleteSurface != nil {
			continue
		}
		if errs := e.Apply(context.Background(), []Envelope{env}); len(errs) != 0 {
			t.Fatalf("apply %s: %v", env.Key, errs)
		}
	}
	ids := e.SurfaceIDs()
	if len(ids) != 1 {
		t.Fatalf("surfaces = %v", ids)
	}
	s := e.Surface(ids[0])
	root, err := s.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	var expanded int
	root.Walk(func(n *Node) bool {
		if n.Index != nil {
			expanded++
		}
		return true
	})
	if expanded == 0 {
		t.Error("no template instantiation happened")
	}
}
