package render

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	a2ui "github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// buildSurface creates a surface by applying an envelope list.
func buildSurface(t *testing.T, envList string, surfaceID string) *a2ui.Surface {
	t.Helper()
	e := a2ui.NewEngine()
	envs, err := a2ui.DecodeEnvelopes([]byte(envList))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if errs := e.Apply(context.Background(), envs); len(errs) != 0 {
		t.Fatalf("apply: %v", errs)
	}
	s := e.Surface(surfaceID)
	if s == nil {
		t.Fatalf("surface %s missing", surfaceID)
	}
	return s
}

// assertNoLineTooWide fails if any line of out exceeds width display cells.
func assertNoLineTooWide(t *testing.T, out string, width int) {
	t.Helper()
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("line %d width %d > %d: %q", i, w, width, line)
		}
	}
}

// all18Env builds one surface containing every basic component.
const all18Env = `[
 {"version":"v1.0","createSurface":{"surfaceId":"all","catalogId":"https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"}},
 {"version":"v1.0","updateComponents":{"surfaceId":"all","components":[
   {"id":"root","component":"Column","children":["title","media_row","gallery","tabs","modal","divider","form","inputs"]},
   {"id":"title","component":"Text","text":"# **All** ` + "`components`" + `","variant":"h1"},
   {"id":"media_row","component":"Row","children":["img","vid","aud","ic"],"justify":"start"},
   {"id":"img","component":"Image","url":{"path":"/img/url"},"description":{"path":"/img/desc"},"fit":"cover","variant":"smallFeature","weight":1},
   {"id":"vid","component":"Video","url":"https://example.com/v.mp4","posterUrl":"https://example.com/p.jpg","weight":1},
   {"id":"aud","component":"AudioPlayer","url":"https://example.com/a.mp3","description":"Podcast episode 1","weight":1},
   {"id":"ic","component":"Icon","name":"mail","weight":1},
   {"id":"gallery","component":"List","children":{"componentId":"gallery_item","path":"/breeds"},"direction":"horizontal"},
   {"id":"gallery_item","component":"Text","text":{"call":"formatString","args":{"value":"${@index(offset:1)}. ${name}"}}},
   {"id":"tabs","component":"Tabs","tabs":[{"title":"First","child":"tab_body"},{"title":{"path":"/secondTab"},"child":"unused_tab"}]},
   {"id":"tab_body","component":"Card","child":"card_inner"},
   {"id":"card_inner","component":"Column","children":["card_text","btn_primary"]},
   {"id":"card_text","component":"Text","text":{"path":"/company"},"variant":"body"},
   {"id":"btn_primary","component":"Button","child":"btn_label","variant":"primary","action":{"event":{"name":"submit","context":{"id":{"path":"/id"}}}}},
   {"id":"btn_label","component":"Text","text":"Submit"},
   {"id":"unused_tab","component":"Text","text":"never shown"},
   {"id":"modal","component":"Modal","trigger":"modal_trigger","content":"modal_content"},
   {"id":"modal_trigger","component":"Button","child":"modal_label","action":{"event":{"name":"open"}}},
   {"id":"modal_label","component":"Text","text":"Details…"},
   {"id":"modal_content","component":"Text","text":"Modal body content"},
   {"id":"divider","component":"Divider","axis":"horizontal"},
   {"id":"form","component":"Column","children":["name_field","email_field","sub_check"]},
   {"id":"name_field","component":"TextField","label":"Name","value":{"path":"/form/name"},"placeholder":"Your name","variant":"shortText"},
   {"id":"email_field","component":"TextField","label":"Email","value":{"path":"/form/email"},"placeholder":"a@b.c","variant":"shortText","checks":[{"call":"email","args":{"value":{"path":"/form/email"}},"message":"Please enter a valid email address."}]},
   {"id":"sub_check","component":"CheckBox","label":"Subscribe to newsletter","value":{"path":"/form/subscribe"}},
   {"id":"inputs","component":"Row","children":["choice","slider","dt"], "justify":"start"},
   {"id":"choice","component":"ChoicePicker","label":"Contact method","variant":"mutuallyExclusive","options":[{"label":"Email","value":"email"},{"label":"Phone","value":"phone"}],"value":{"path":"/form/preference"},"displayStyle":"checkbox","weight":1},
   {"id":"slider","component":"Slider","label":"Volume","min":0,"max":10,"value":{"path":"/form/volume"},"steps":10,"weight":1},
   {"id":"dt","component":"DateTimeInput","value":{"path":"/form/when"},"enableDate":true,"enableTime":true,"label":"When","weight":1}
 ]}},
 {"version":"v1.0","updateDataModel":{"surfaceId":"all","value":{
   "company":"Acme Corp","id":"42","secondTab":"Second",
   "img":{"url":"https://example.com/cat.jpg","desc":"A cat"},
   "breeds":[{"name":"Poodle"},{"name":"Labrador"},{"name":"Corgi"}],
   "form":{"name":"Jane Doe","email":"not-an-email","subscribe":true,"preference":["email"],"volume":3,"when":"2026-09-11T10:30"}
 }}}
]`

func TestRenderAll18(t *testing.T) {
	s := buildSurface(t, all18Env, "all")
	out := Render(s, 80)
	if out == "" {
		t.Fatal("empty render")
	}
	assertNoLineTooWide(t, out, 80)

	// Content expectations: every component family leaves a trace. Media
	// placeholders wrap inside their weighted Row columns, so each is
	// asserted piecewise.
	expect := []string{
		"All components", // markdown stripped from title
		"[image",
		"https://example.com",
		"/cat.jpg — A cat]",
		"[video",
		"/v.mp4]",
		"[audio",
		"/a.mp3 — Podcast",
		"✉",                // mail icon glyph
		"1. Poodle",        // template + @index(offset:1)
		"3. Corgi",         // third element resolves its own scope
		"[First] | Second", // tab bar, active first
		"Acme Corp",
		"[[ Submit ]] → submit", // primary button + action name
		"Name: [Jane Doe]",
		"Email: [not-an-email]",
		"✗ Please enter a valid email address.", // failing check + rule message
		"[x] Subscribe to newsletter",
		"Contact method:",
		"(•) Email",
		"( ) Phone",
		"Volume: 3/10",
		"When: 2026-09-11T10:30",
		"modal",
	}
	for _, want := range expect {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q", want)
		}
	}
	// Modal trigger and indented content.
	if !strings.Contains(out, "Details…") || !strings.Contains(out, "  Modal body content") {
		t.Error("modal trigger/content missing")
	}
	// The inactive tab body must not appear.
	if strings.Contains(out, "never shown") {
		t.Error("inactive tab content leaked into static render")
	}
	// Stable snapshot.
	t.Logf("render output:\n%s", out)
}

func TestRenderSnapshotStable(t *testing.T) {
	s := buildSurface(t, all18Env, "all")
	first := Render(s, 60)
	second := Render(s, 60)
	if first != second {
		t.Fatal("render is not deterministic for identical input")
	}
	assertNoLineTooWide(t, first, 60)
	// Different widths must adapt (divider line length).
	narrow := Render(s, 20)
	assertNoLineTooWide(t, narrow, 20)
	if !strings.Contains(narrow, "\n") {
		t.Error("narrow render did not wrap")
	}
}

func TestRenderHeightBudget(t *testing.T) {
	// Build a surface with more content than the budget.
	var b strings.Builder
	b.WriteString(`[{"version":"v1.0","createSurface":{"surfaceId":"big","components":[{"id":"root","component":"Column","children":[`)
	for i := 0; i < 300; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"t` + jsonInt(i) + `"`)
	}
	b.WriteString(`]}`)
	for i := 0; i < 300; i++ {
		b.WriteString(`,{"id":"t` + jsonInt(i) + `","component":"Text","text":"line ` + jsonInt(i) + `"}`)
	}
	b.WriteString(`]}}]`)
	s := buildSurface(t, b.String(), "big")
	out := RenderWithOptions(s, 40, Options{MaxHeight: 12})
	lines := strings.Split(out, "\n")
	if len(lines) > 12 {
		t.Fatalf("height budget exceeded: %d lines", len(lines))
	}
	if lines[len(lines)-1] != "(full view: focus surface)" {
		t.Errorf("truncation footer missing, last line %q", lines[len(lines)-1])
	}
	if lines[len(lines)-2] != "…" {
		t.Errorf("ellipsis marker missing, got %q", lines[len(lines)-2])
	}
}

func jsonInt(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

func TestRenderUnknownAndPlaceholder(t *testing.T) {
	s := buildSurface(t, `[{"version":"v1.0","createSurface":{"surfaceId":"u","components":[
		{"id":"root","component":"Column","children":["ghost","mystery"]},
		{"id":"mystery","component":"Hologram","intensity":11,"label":"weird"}
	]}}]`, "u")
	out := Render(s, 60)
	assertNoLineTooWide(t, out, 60)
	if !strings.Contains(out, `unresolved component "ghost"`) {
		t.Errorf("placeholder missing:\n%s", out)
	}
	if !strings.Contains(out, `unsupported component "Hologram"`) {
		t.Errorf("unknown component box missing:\n%s", out)
	}
	// The unknown component's JSON peek appears (bounded).
	if !strings.Contains(out, "intensity") {
		t.Errorf("JSON peek missing:\n%s", out)
	}
}

func TestRenderEmptyAndHostile(t *testing.T) {
	// No root yet: progressive rendering shows nothing, no panic.
	s := a2ui.NewSurface("empty")
	if out := Render(s, 40); out != "" {
		t.Errorf("rootless surface rendered %q", out)
	}
	if out := Render(nil, 40); out != "" {
		t.Error("nil surface should render empty")
	}
	// Cycle must not hang or panic.
	var cyc string
	_ = json.Unmarshal([]byte(`[{"id":"root","component":"Column","children":["a"]},{"id":"a","component":"Column","children":["root"]}]`), &cyc)
	e := a2ui.NewEngine()
	envs, err := a2ui.DecodeEnvelopes([]byte(`[{"version":"v1.0","createSurface":{"surfaceId":"c","components":[{"id":"root","component":"Column","children":["a"]},{"id":"a","component":"Column","children":["root"]}]}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if errs := e.Apply(context.Background(), envs); len(errs) != 0 {
		t.Fatal(errs)
	}
	out := Render(e.Surface("c"), 40)
	assertNoLineTooWide(t, out, 40)
}

func TestStripMarkdown(t *testing.T) {
	cases := map[string]string{
		"# Heading":                   "Heading",
		"**bold** and *italic*":       "bold and italic",
		"`code`":                      "code",
		"[text](https://example.com)": "text",
		"plain":                       "plain",
		"escaped \\*literal\\*":       "escaped *literal*",
		"multi\n# line heading":       "multi\nline heading",
	}
	for in, want := range cases {
		if got := StripMarkdown(in); got != want {
			t.Errorf("StripMarkdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIconGlyphsComplete(t *testing.T) {
	if len(iconGlyphs) != iconCount {
		t.Errorf("glyph map has %d entries, expected %d", len(iconGlyphs), iconCount)
	}
	if Glyph("notAnIcon") != "◦" {
		t.Error("unknown icon should map to the hollow bullet")
	}
	for _, name := range []string{"check", "close", "mail", "warning", "star"} {
		if Glyph(name) == "◦" {
			t.Errorf("known icon %q mapped to fallback", name)
		}
	}
}

// TestVendoredFixturesRender renders every vendored spec fixture without
// panics and within width budgets.
func TestVendoredFixturesRender(t *testing.T) {
	files := []string{
		"../testdata/contact_form_example.jsonl",
		"../testdata/example_01_flight_status.json",
		"../testdata/example_09_login_form.json",
		"../testdata/example_34_child_list_template.json",
		"../testdata/example_36_modal.json",
	}
	for _, file := range files {
		raw := readFileForTest(t, file)
		var envs []a2ui.Envelope
		var wrapper struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(raw, &wrapper); err == nil && len(wrapper.Messages) > 0 {
			envs, err = a2ui.DecodeEnvelopes(wrapper.Messages)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
		} else {
			var lines []string
			for _, line := range strings.Split(string(raw), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					lines = append(lines, line)
				}
			}
			for _, line := range lines {
				batch, err := a2ui.DecodeEnvelopes([]byte(line))
				if err != nil {
					t.Fatalf("%s: %v", file, err)
				}
				envs = append(envs, batch...)
			}
		}
		e := a2ui.NewEngine()
		for _, env := range envs {
			if env.DeleteSurface != nil {
				continue
			}
			_ = e.Apply(context.Background(), []a2ui.Envelope{env})
		}
		for _, id := range e.SurfaceIDs() {
			for _, width := range []int{30, 60, 100} {
				out := Render(e.Surface(id), width)
				assertNoLineTooWide(t, out, width)
				if out == "" {
					t.Errorf("%s/%s: empty render at width %d", file, id, width)
				}
			}
		}
	}
}
