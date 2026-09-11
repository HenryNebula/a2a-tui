package a2ui

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func surfaceFrom(t *testing.T, compsJSON, dataJSON string) *Surface {
	t.Helper()
	s := NewSurface("test")
	var comps []Component
	if err := json.Unmarshal([]byte(compsJSON), &comps); err != nil {
		t.Fatalf("components: %v", err)
	}
	for i := range comps {
		s.Components[comps[i].ID] = &comps[i]
	}
	if dataJSON != "" {
		if err := json.Unmarshal([]byte(dataJSON), &s.DataModel); err != nil {
			t.Fatalf("data model: %v", err)
		}
	}
	return s
}

// TestTemplateScopeResolution pins the spec's collection-scope rules using
// the worked example from the protocol spec (v1.0 "Path resolution & scope"):
// relative paths resolve against the template element; absolute paths always
// resolve against the root model.
func TestTemplateScopeResolution(t *testing.T) {
	s := surfaceFrom(t, `[
		{"id":"root","component":"List","children":{"path":"/employees","componentId":"employee_card_template"}},
		{"id":"employee_card_template","component":"Column","children":["name_text","company_text"]},
		{"id":"name_text","component":"Text","text":{"path":"name"}},
		{"id":"company_text","component":"Text","text":{"path":"/company"}}
	]`, `{"company":"Acme Corp","employees":[{"name":"Alice","role":"Engineer"},{"name":"Bob","role":"Designer"}]}`)

	root, err := s.Materialize()
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if root.Component.ID != "root" {
		t.Fatalf("root = %s", root.Component.ID)
	}
	if len(root.Children) != 2 {
		t.Fatalf("template expanded to %d children, want 2", len(root.Children))
	}
	ctx := s.EvalContextFor(NewFunctionRegistry())
	wantNames := []string{"Alice", "Bob"}
	for i, child := range root.Children {
		if child.Scope == nil {
			t.Fatalf("child %d has no scope", i)
		}
		if child.Index == nil || *child.Index != i {
			t.Fatalf("child %d index = %v", i, child.Index)
		}
		// name_text: relative path against the element.
		nameNode := child.Children[0]
		got := nameNode.Component.Props.(*TextProps).Text.EvalString(nameNode.EvalContext(ctx))
		if got != wantNames[i] {
			t.Errorf("child %d name = %q, want %q", i, got, wantNames[i])
		}
		// company_text: absolute path against the root.
		companyNode := child.Children[1]
		got = companyNode.Component.Props.(*TextProps).Text.EvalString(companyNode.EvalContext(ctx))
		if got != "Acme Corp" {
			t.Errorf("child %d company = %q, want Acme Corp", i, got)
		}
	}
}

// TestTemplateIndexFunction verifies @index resolves per element.
func TestTemplateIndexFunction(t *testing.T) {
	s := surfaceFrom(t, `[
		{"id":"root","component":"List","children":{"path":"/items","componentId":"row"}},
		{"id":"row","component":"Text","text":{"call":"formatString","args":{"value":"#${@index(offset:1)}: ${label}"}}}
	]`, `{"items":[{"label":"a"},{"label":"b"},{"label":"c"}]}`)
	root, err := s.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	ctx := s.EvalContextFor(NewFunctionRegistry())
	want := []string{"#1: a", "#2: b", "#3: c"}
	for i, child := range root.Children {
		tp := child.Component.Props.(*TextProps)
		if got := tp.Text.EvalString(child.EvalContext(ctx)); got != want[i] {
			t.Errorf("item %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestMaterializePlaceholders(t *testing.T) {
	s := surfaceFrom(t, `[
		{"id":"root","component":"Column","children":["missing_child","present"]},
		{"id":"present","component":"Text","text":"ok"}
	]`, ``)
	root, err := s.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	if !root.Children[0].Placeholder || root.Children[0].RefID != "missing_child" {
		t.Error("unresolved child should be a placeholder")
	}
	if root.Children[1].Placeholder {
		t.Error("resolved child should not be a placeholder")
	}
	// Missing list data renders a placeholder, not an error.
	s2 := surfaceFrom(t, `[
		{"id":"root","component":"List","children":{"path":"/nope","componentId":"t"}},
		{"id":"t","component":"Text","text":"x"}
	]`, `{}`)
	root2, err := s2.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	if len(root2.Children) != 1 || !root2.Children[0].Placeholder {
		t.Error("missing template data should yield one placeholder")
	}
}

func TestMaterializeNoRoot(t *testing.T) {
	s := surfaceFrom(t, `[{"id":"orphan","component":"Text","text":"x"}]`, ``)
	if _, err := s.Materialize(); err == nil {
		t.Fatal("materialize without root should error")
	}
}

// TestMaterializeCycle verifies cycles are cut instead of hanging.
func TestMaterializeCycle(t *testing.T) {
	s := surfaceFrom(t, `[
		{"id":"root","component":"Column","children":["a"]},
		{"id":"a","component":"Column","children":["b"]},
		{"id":"b","component":"Column","children":["a"]}
	]`, ``)
	root, err := s.Materialize() // must terminate
	if err != nil {
		t.Fatalf("cycle materialize: %v", err)
	}
	var placeholders int
	root.Walk(func(n *Node) bool {
		if n.Placeholder {
			placeholders++
		}
		return true
	})
	if placeholders == 0 {
		t.Error("cycle should produce at least one placeholder")
	}
	// Self reference.
	s2 := surfaceFrom(t, `[{"id":"root","component":"Column","children":["root"]}]`, ``)
	if _, err := s2.Materialize(); err != nil {
		t.Fatalf("self cycle: %v", err)
	}
}

// TestMaterializeSharedSubtree verifies DAG reuse (same child under two
// parents) stays legal — only true cycles are cut.
func TestMaterializeSharedSubtree(t *testing.T) {
	s := surfaceFrom(t, `[
		{"id":"root","component":"Column","children":["left","right"]},
		{"id":"left","component":"Card","child":"shared"},
		{"id":"right","component":"Card","child":"shared"},
		{"id":"shared","component":"Text","text":"shared"}
	]`, ``)
	root, err := s.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	var sharedCount int
	root.Walk(func(n *Node) bool {
		if n.Component != nil && n.Component.ID == "shared" && !n.Placeholder {
			sharedCount++
		}
		return true
	})
	if sharedCount != 2 {
		t.Errorf("shared subtree rendered %d times, want 2", sharedCount)
	}
}

// TestMaterializeDeepNesting verifies the depth cap kicks in without hanging.
func TestMaterializeDeepNesting(t *testing.T) {
	var b strings.Builder
	b.WriteString(`[{"id":"root","component":"Column","children":["c0"]}`)
	for i := 0; i < 500; i++ {
		b.WriteString(`,{"id":"c` + strconv.Itoa(i) + `","component":"Column","children":["c` + strconv.Itoa(i+1) + `"]}`)
	}
	b.WriteString(`,{"id":"c500","component":"Text","text":"deep"}]`)
	s := surfaceFrom(t, b.String(), ``)
	root, err := s.Materialize()
	if err != nil {
		t.Fatalf("deep nesting: %v", err)
	}
	var placeholders int
	root.Walk(func(n *Node) bool {
		if n.Placeholder {
			placeholders++
		}
		return true
	})
	if placeholders == 0 {
		t.Error("depth cap should have cut the chain")
	}
}

// TestTemplateExpansionCap guards hostile data: 5000 items are capped.
func TestTemplateExpansionCap(t *testing.T) {
	items := make([]string, 0, 5000)
	for i := 0; i < 5000; i++ {
		items = append(items, `{"n":`+strconv.Itoa(i)+`}`)
	}
	data := `{"items":[` + strings.Join(items, ",") + `]}`
	s := surfaceFrom(t, `[
		{"id":"root","component":"List","children":{"path":"/items","componentId":"t"}},
		{"id":"t","component":"Text","text":"x"}
	]`, data)
	root, err := s.Materialize()
	if err != nil {
		t.Fatalf("capped expansion: %v", err)
	}
	if len(root.Children) > MaxTemplateItems {
		t.Errorf("expanded %d children, cap is %d", len(root.Children), MaxTemplateItems)
	}
}
