package widget

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/jsonptr"
)

// ---------------------------------------------------------------------------
// Test surface builders (same shapes the fixture agent emits)
// ---------------------------------------------------------------------------

func lit(v any) a2ui.Dynamic {
	return a2ui.Dynamic{Kind: a2ui.KindLiteral, Literal: v}
}

func bind(path string) a2ui.Dynamic {
	return a2ui.Dynamic{Kind: a2ui.KindPath, Path: path}
}

func column(id string, children ...string) a2ui.Component {
	return a2ui.Component{ID: id, Component: "Column", Props: &a2ui.ColumnProps{
		Children: a2ui.ChildList{IDs: children},
	}}
}

func textComponent(id, text, variant string) a2ui.Component {
	return a2ui.Component{ID: id, Component: "Text", Props: &a2ui.TextProps{
		Text:    lit(text),
		Variant: variant,
	}}
}

func requiredCheck(path, message string) a2ui.CheckRule {
	return a2ui.CheckRule{
		Condition: a2ui.Dynamic{Kind: a2ui.KindCall, Call: &a2ui.Call{
			Name: "required",
			Args: map[string]any{"value": map[string]any{"path": path}},
		}},
		Message: message,
	}
}

func f64(v float64) *float64 { return &v }
func i64(v int) *int         { return &v }

// formSurface mirrors the fixture agent's "a2ui form" surface (all input
// components; slightly trimmed) with sendDataModel configurable.
func formSurface(id string, sendDataModel bool) *a2ui.CreateSurface {
	return &a2ui.CreateSurface{
		SurfaceID:     id,
		CatalogID:     a2ui.BasicCatalogIDV1,
		SendDataModel: sendDataModel,
		Components: []a2ui.Component{
			column("root", "title",
				"name-field", "age-field", "subscribe-check", "plan-picker",
				"interests-picker", "volume-slider", "plain-slider", "meeting-at",
				"submit-btn"),
			textComponent("title", "Contact form", "h3"),
			{ID: "name-field", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label:   lit("Name"),
				Value:   bind("/name"),
				Variant: "shortText",
				Checkable: a2ui.Checkable{Checks: []a2ui.CheckRule{
					requiredCheck("/name", "Name is required"),
				}},
			}},
			{ID: "age-field", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label:   lit("Age"),
				Value:   bind("/age"),
				Variant: "number",
			}},
			{ID: "subscribe-check", Component: "CheckBox", Props: &a2ui.CheckBoxProps{
				Label: lit("Subscribe"),
				Value: bind("/subscribe"),
			}},
			{ID: "plan-picker", Component: "ChoicePicker", Props: &a2ui.ChoicePickerProps{
				Label:   lit("Plan"),
				Variant: "singleSelection",
				Options: []a2ui.ChoiceOption{
					{Label: lit("Free"), Value: "free"},
					{Label: lit("Basic"), Value: "basic"},
					{Label: lit("Pro"), Value: "pro"},
				},
				Value: bind("/plan"),
			}},
			{ID: "interests-picker", Component: "ChoicePicker", Props: &a2ui.ChoicePickerProps{
				Label:      lit("Interests"),
				Variant:    "multipleSelection",
				Filterable: true,
				Options: []a2ui.ChoiceOption{
					{Label: lit("A2A"), Value: "a2a"},
					{Label: lit("A2UI"), Value: "a2ui"},
					{Label: lit("Terminals"), Value: "tui"},
				},
				Value: bind("/interests"),
			}},
			{ID: "volume-slider", Component: "Slider", Props: &a2ui.SliderProps{
				Label: lit("Volume"),
				Min:   f64(0), Max: f64(11), Steps: i64(11),
				Value: bind("/volume"),
			}},
			{ID: "plain-slider", Component: "Slider", Props: &a2ui.SliderProps{
				Label: lit("Plain"),
				Min:   f64(0), Max: f64(100),
				Value: bind("/plain"),
			}},
			{ID: "meeting-at", Component: "DateTimeInput", Props: &a2ui.DateTimeInputProps{
				Label:      lit("Meeting"),
				Value:      bind("/meetingAt"),
				EnableDate: true, EnableTime: true,
			}},
			{ID: "submit-btn", Component: "Button", Props: &a2ui.ButtonProps{
				Child: "submit-btn-label",
				Checkable: a2ui.Checkable{Checks: []a2ui.CheckRule{
					requiredCheck("/name", "Name is required"),
				}},
				Action: &a2ui.ActionSpec{Event: &a2ui.ActionEvent{Name: "submit"}},
			}},
			textComponent("submit-btn-label", "Submit", "body"),
		},
		DataModel: map[string]any{
			"name":      "",
			"age":       nil,
			"subscribe": true,
			"plan":      "pro",
			"interests": []any{"a2a"},
			"volume":    7,
			"plain":     50,
			"meetingAt": "2026-10-01T09:00",
		},
	}
}

// buildForm applies a createSurface envelope through a real engine and
// returns the widget over the resulting live surface.
func buildForm(t *testing.T, id string, sendDataModel bool) (*Model, *a2ui.Engine) {
	t.Helper()
	eng := a2ui.NewEngine()
	raw, err := json.Marshal(map[string]any{"version": a2ui.VersionV1, "createSurface": formSurface(id, sendDataModel)})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := a2ui.DecodeEnvelopes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if errs := eng.Apply(context.Background(), envs); len(errs) > 0 {
		t.Fatalf("apply: %v", errs)
	}
	m, err := New(eng.Surface(id))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m, eng
}

// applyUpdateDataModel simulates an agent-side data-model write.
func applyUpdateDataModel(t *testing.T, eng *a2ui.Engine, surfaceID, path string, value any) {
	t.Helper()
	payload := map[string]any{"surfaceId": surfaceID, "value": value}
	if path != "" {
		payload["path"] = path
	}
	raw, err := json.Marshal(map[string]any{"version": a2ui.VersionV1, "updateDataModel": payload})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := a2ui.DecodeEnvelopes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if errs := eng.Apply(context.Background(), envs); len(errs) > 0 {
		t.Fatalf("apply updateDataModel: %v", errs)
	}
}

// key drives one key through the widget, failing on binding errors.
func key(t *testing.T, m *Model, msg tea.KeyMsg) ActionOut {
	t.Helper()
	out, _, err := m.Update(msg)
	if err != nil {
		t.Fatalf("update %q: %v", msg.String(), err)
	}
	return out
}

func runes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func press(keyType tea.KeyType) tea.KeyMsg {
	return tea.KeyMsg{Type: keyType}
}

func space() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
}

// get reads the surface data model at an absolute path.
func get(t *testing.T, m *Model, path string) any {
	t.Helper()
	v, ok := jsonptr.Get(m.surf.DataModel, path)
	if !ok {
		return nil
	}
	return v
}

// tab advances focus n times.
func tab(t *testing.T, m *Model, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key(t, m, press(tea.KeyTab))
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFocusOrderAndKinds(t *testing.T) {
	m, _ := buildForm(t, "focus", false)
	want := []string{
		"name-field", "age-field", "subscribe-check", "plan-picker",
		"interests-picker", "volume-slider", "plain-slider", "meeting-at",
		"submit-btn",
	}
	if len(m.focusables) != len(want) {
		t.Fatalf("focusables = %d, want %d", len(m.focusables), len(want))
	}
	for i, id := range want {
		if m.focusables[i].id != id {
			t.Fatalf("focusable[%d] = %q, want %q", i, m.focusables[i].id, id)
		}
	}
	if m.FocusedID() != "name-field" {
		t.Fatalf("initial focus = %q", m.FocusedID())
	}
	// Shift+tab wraps backwards to the button.
	key(t, m, press(tea.KeyShiftTab))
	if m.FocusedID() != "submit-btn" {
		t.Fatalf("shift+tab focus = %q", m.FocusedID())
	}
}

func TestTextFieldEditWritesDataModel(t *testing.T) {
	m, _ := buildForm(t, "text", false)
	key(t, m, runes("Ada"))
	if v := get(t, m, "/name"); v != "Ada" {
		t.Fatalf("/name = %#v, want Ada", v)
	}
	if !m.Dirty() {
		t.Fatal("model should be dirty after an edit")
	}
	// Backspace rewrites the shorter value.
	key(t, m, press(tea.KeyBackspace))
	if v := get(t, m, "/name"); v != "Ad" {
		t.Fatalf("/name = %#v, want Ad", v)
	}
	// The required check hint shows while empty: clear and inspect the view.
	key(t, m, press(tea.KeyBackspace))
	key(t, m, press(tea.KeyBackspace))
	view := m.View(80, 0)
	if !strings.Contains(view, "Name is required") {
		t.Fatalf("check hint missing:\n%s", view)
	}
}

func TestTextFieldNumberVariant(t *testing.T) {
	m, _ := buildForm(t, "number", false)
	tab(t, m, 1) // age-field
	key(t, m, runes("abc"))
	if v, ok := jsonptr.Get(m.surf.DataModel, "/age"); ok && v != nil {
		t.Fatalf("/age = %#v, want untouched", v)
	}
	if view := m.View(80, 0); !strings.Contains(view, "must be a number") {
		t.Fatalf("number hint missing:\n%s", view)
	}
	key(t, m, press(tea.KeyBackspace))
	key(t, m, press(tea.KeyBackspace))
	key(t, m, press(tea.KeyBackspace))
	key(t, m, runes("42"))
	if v := get(t, m, "/age"); v != float64(42) {
		t.Fatalf("/age = %#v, want 42", v)
	}
}

func TestCheckBoxToggle(t *testing.T) {
	m, _ := buildForm(t, "check", false)
	tab(t, m, 2) // subscribe-check
	if v := get(t, m, "/subscribe"); v != true {
		t.Fatalf("initial /subscribe = %#v", v)
	}
	key(t, m, space())
	if v := get(t, m, "/subscribe"); v != false {
		t.Fatalf("/subscribe = %#v, want false", v)
	}
	key(t, m, press(tea.KeyEnter))
	if v := get(t, m, "/subscribe"); v != true {
		t.Fatalf("/subscribe = %#v, want true", v)
	}
}

func TestChoicePickerSingleSelect(t *testing.T) {
	m, _ := buildForm(t, "single", false)
	tab(t, m, 3) // plan-picker (cursor on Free)
	key(t, m, press(tea.KeyDown))
	key(t, m, space())
	if v := get(t, m, "/plan"); v != "basic" {
		t.Fatalf("/plan = %#v, want basic", v)
	}
	// Enter selects the next option under the cursor.
	key(t, m, press(tea.KeyDown))
	key(t, m, press(tea.KeyEnter))
	if v := get(t, m, "/plan"); v != "pro" {
		t.Fatalf("/plan = %#v, want pro", v)
	}
}

func TestChoicePickerMultiSelectAndFilter(t *testing.T) {
	m, _ := buildForm(t, "multi", false)
	tab(t, m, 4)       // interests-picker; /interests = ["a2a"], cursor at A2A
	key(t, m, space()) // toggle A2A off
	if v := get(t, m, "/interests"); len(v.([]any)) != 0 {
		t.Fatalf("/interests = %#v, want empty", v)
	}
	key(t, m, space()) // toggle A2A back on
	if v := get(t, m, "/interests"); len(v.([]any)) != 1 {
		t.Fatalf("/interests = %#v, want [a2a]", v)
	}
	// Filter narrows the visible options; space toggles the match.
	key(t, m, runes("a2u"))
	view := m.View(80, 0)
	if strings.Contains(view, "A2A\n") || !strings.Contains(view, "A2UI") {
		// (A2A may still appear inside the filter echo; require A2UI visible.)
		t.Fatalf("filter view missing A2UI:\n%s", view)
	}
	if strings.Count(view, "❯") != 1 {
		t.Fatalf("expected exactly one option cursor:\n%s", view)
	}
	key(t, m, space()) // toggle A2UI on
	v := get(t, m, "/interests").([]any)
	if len(v) != 2 || v[0] != "a2a" || v[1] != "a2ui" {
		t.Fatalf("/interests = %#v, want [a2a a2ui]", v)
	}
}

func TestSliderStepping(t *testing.T) {
	m, _ := buildForm(t, "slider", false)
	tab(t, m, 5) // volume-slider: 0..11 in 11 steps, value 7
	key(t, m, press(tea.KeyRight))
	if v := get(t, m, "/volume"); v != float64(8) {
		t.Fatalf("/volume = %#v, want 8", v)
	}
	key(t, m, press(tea.KeyLeft))
	key(t, m, press(tea.KeyLeft))
	if v := get(t, m, "/volume"); v != float64(6) {
		t.Fatalf("/volume = %#v, want 6", v)
	}
	// Up moves 10% of the range (1.1), snapped back onto the step grid.
	key(t, m, press(tea.KeyUp))
	if v := get(t, m, "/volume"); v != float64(7) {
		t.Fatalf("/volume = %#v, want 7 after 10%% snap", v)
	}

	tab(t, m, 1) // plain-slider: 0..100, no steps, value 50
	key(t, m, press(tea.KeyRight))
	if v := get(t, m, "/plain"); v != float64(51) {
		t.Fatalf("/plain = %#v, want 51", v)
	}
	key(t, m, press(tea.KeyUp))
	if v := get(t, m, "/plain"); math.Abs(v.(float64)-61) > 0.001 {
		t.Fatalf("/plain = %#v, want ~61", v)
	}
	// Clamped at max.
	for i := 0; i < 10; i++ {
		key(t, m, press(tea.KeyUp))
	}
	if v := get(t, m, "/plain"); v != float64(100) {
		t.Fatalf("/plain = %#v, want clamped 100", v)
	}
}

func TestDateTimeValidationAndWrite(t *testing.T) {
	m, _ := buildForm(t, "dt", false)
	tab(t, m, 7) // meeting-at, seeded "2026-10-01T09:00"
	// Clear the field.
	for i := 0; i < len("2026-10-01T09:00"); i++ {
		key(t, m, press(tea.KeyBackspace))
	}
	key(t, m, runes("not-a-date"))
	if v, ok := jsonptr.Get(m.surf.DataModel, "/meetingAt"); ok && v != nil {
		t.Fatalf("/meetingAt = %#v, want untouched", v)
	}
	if view := m.View(80, 0); !strings.Contains(view, "invalid date/time") {
		t.Fatalf("date hint missing:\n%s", view)
	}
	for i := 0; i < len("not-a-date"); i++ {
		key(t, m, press(tea.KeyBackspace))
	}
	key(t, m, runes("2027-01-15T10:30"))
	if v := get(t, m, "/meetingAt"); v != "2027-01-15T10:30" {
		t.Fatalf("/meetingAt = %#v", v)
	}
}

func TestButtonChecksGateActivation(t *testing.T) {
	m, _ := buildForm(t, "button", false)
	// /name is empty: the button's required check fails and enter must not
	// fire anything.
	tab(t, m, 8) // submit-btn
	out := key(t, m, press(tea.KeyEnter))
	if out.Action != nil || out.LocalFunctionCall != nil {
		t.Fatalf("failing-checks button fired: %+v", out)
	}
	view := m.View(80, 0)
	if !strings.Contains(view, "(disabled)") || !strings.Contains(view, "Name is required") {
		t.Fatalf("disabled hints missing:\n%s", view)
	}
	if !strings.Contains(view, "checks failed") {
		t.Fatalf("activation flash missing:\n%s", view)
	}
}

func TestButtonFiresActionWithDataModel(t *testing.T) {
	m, _ := buildForm(t, "fire", true) // sendDataModel
	key(t, m, runes("Grace"))          // /name filled → checks pass
	tab(t, m, 8)
	out := key(t, m, press(tea.KeyEnter))
	if out.Action == nil {
		t.Fatal("button did not fire")
	}
	if out.Action.Name != "submit" || out.Action.SurfaceID != "fire" ||
		out.Action.SourceComponentID != "submit-btn" {
		t.Fatalf("action = %+v", out.Action)
	}
	if out.Action.Timestamp == "" {
		t.Fatal("action timestamp missing")
	}
	if out.EnvelopeVersion != a2ui.VersionV1 {
		t.Fatalf("version = %q", out.EnvelopeVersion)
	}
	dm, ok := out.DataModel.(map[string]any)
	if !ok || dm["name"] != "Grace" {
		t.Fatalf("data model snapshot = %#v", out.DataModel)
	}
	if m.Dirty() {
		t.Fatal("dirty should clear after the action carries the edits out")
	}
	// The action JSON must round-trip as a renderer message.
	enc, err := json.Marshal(a2ui.RendererMessage{Version: out.EnvelopeVersion, Action: out.Action})
	if err != nil {
		t.Fatal(err)
	}
	var back a2ui.RendererMessage
	if err := json.Unmarshal(enc, &back); err != nil || back.Action == nil || back.Action.Name != "submit" {
		t.Fatalf("action round-trip failed: %v %v", err, back.Action)
	}
}

func TestButtonLocalFunctionCall(t *testing.T) {
	m, eng := buildForm(t, "fn", false)
	key(t, m, runes("Ada"))
	tab(t, m, 8)
	// Swap the action for a local function call (updateComponents-style).
	applyUpdateComponents(t, eng, "fn", a2ui.Component{ID: "submit-btn", Component: "Button",
		Props: &a2ui.ButtonProps{
			Child: "submit-btn-label",
			Checkable: a2ui.Checkable{Checks: []a2ui.CheckRule{
				requiredCheck("/name", "Name is required"),
			}},
			Action: &a2ui.ActionSpec{FunctionCall: &a2ui.Call{
				Name: "openUrl",
				Args: map[string]any{"url": "https://example.com/docs"},
			}},
		}})
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	out := key(t, m, press(tea.KeyEnter))
	if out.Action != nil {
		t.Fatal("event action set for functionCall button")
	}
	if out.LocalFunctionCall == nil || out.LocalFunctionCall.Name != "openUrl" {
		t.Fatalf("local call = %+v", out.LocalFunctionCall)
	}
}

// applyUpdateComponents simulates an agent-side component upsert.
func applyUpdateComponents(t *testing.T, eng *a2ui.Engine, surfaceID string, comps ...a2ui.Component) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"version":          a2ui.VersionV1,
		"updateComponents": map[string]any{"surfaceId": surfaceID, "components": comps},
	})
	if err != nil {
		t.Fatal(err)
	}
	envs, err := a2ui.DecodeEnvelopes(raw)
	if err != nil {
		t.Fatal(err)
	}
	if errs := eng.Apply(context.Background(), envs); len(errs) > 0 {
		t.Fatalf("apply updateComponents: %v", errs)
	}
}

func TestRefreshAdoptsExternalOverwrite(t *testing.T) {
	m, eng := buildForm(t, "r1", false)
	key(t, m, runes("Ada"))
	applyUpdateDataModel(t, eng, "r1", "/name", "External")
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	view := m.View(80, 0)
	if strings.Contains(view, "Ada") {
		t.Fatalf("user value kept despite external overwrite:\n%s", view)
	}
	if !strings.Contains(view, "External") {
		t.Fatalf("external value not adopted:\n%s", view)
	}
	if m.Dirty() {
		t.Fatal("dirty should clear when the external value is adopted")
	}
}

func TestRefreshKeepsUnacknowledgedUserValue(t *testing.T) {
	m, eng := buildForm(t, "r2", false)
	key(t, m, runes("Ada"))
	// The agent writes back exactly what we last wrote: keep the editor
	// (still dirty, value still ours).
	applyUpdateDataModel(t, eng, "r2", "/name", "Ada")
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if !m.Dirty() {
		t.Fatal("dirty lost on echo refresh — user value would be dropped")
	}
	if view := m.View(80, 0); !strings.Contains(view, "Ada") {
		t.Fatalf("user value not preserved:\n%s", view)
	}
	// A differing external write still wins.
	applyUpdateDataModel(t, eng, "r2", "/name", "Changed")
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if view := m.View(80, 0); !strings.Contains(view, "Changed") {
		t.Fatalf("external change not adopted:\n%s", view)
	}
	if m.Dirty() {
		t.Fatal("dirty should clear after adopting the external change")
	}
}

func TestRefreshPreservesFocusByID(t *testing.T) {
	m, eng := buildForm(t, "r3", false)
	tab(t, m, 3) // plan-picker
	if m.FocusedID() != "plan-picker" {
		t.Fatalf("focus = %q", m.FocusedID())
	}
	// Agent upsert touches the data model (layout unchanged).
	applyUpdateDataModel(t, eng, "r3", "/plan", "free")
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if m.FocusedID() != "plan-picker" {
		t.Fatalf("focus after refresh = %q", m.FocusedID())
	}
	// After the agent replaces the button with a text, the picker keeps
	// focus and the new text renders.
	applyUpdateComponents(t, eng, "r3", textComponent("submit-btn", "Submitted ✓", "body"))
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	if m.FocusedID() != "plan-picker" {
		t.Fatalf("focus after button removal = %q", m.FocusedID())
	}
	if view := m.View(80, 0); !strings.Contains(view, "Submitted ✓") {
		t.Fatalf("agent text missing after refresh:\n%s", view)
	}
}

func TestUntrustedContentSanitizedInView(t *testing.T) {
	m, eng := buildForm(t, "san", false)
	dirty := "\x1b[31mRed\x1b[0m\x07text\nwith controls"
	applyUpdateComponents(t, eng, "san", textComponent("title", dirty, "body"))
	if err := m.Refresh(); err != nil {
		t.Fatal(err)
	}
	view := m.View(80, 0)
	if strings.ContainsAny(view, "\x1b\x07") {
		t.Fatalf("control bytes survived into the view:\n%q", view)
	}
	if !strings.Contains(view, "Redtext") {
		t.Fatalf("text mangled:\n%s", view)
	}
}

func TestNonPathBoundInputsAreReadOnly(t *testing.T) {
	eng := a2ui.NewEngine()
	cs := &a2ui.CreateSurface{
		SurfaceID: "ro",
		Components: []a2ui.Component{
			column("root", "literal", "relative"),
			{ID: "literal", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Literal"), Value: lit("fixed"),
			}},
			{ID: "relative", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Relative"), Value: bind("name"),
			}},
		},
		DataModel: map[string]any{"name": "x"},
	}
	raw, _ := json.Marshal(map[string]any{"version": a2ui.VersionV1, "createSurface": cs})
	envs, _ := a2ui.DecodeEnvelopes(raw)
	if errs := eng.Apply(context.Background(), envs); len(errs) > 0 {
		t.Fatal(errs)
	}
	m, err := New(eng.Surface("ro"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.focusables) != 0 {
		t.Fatalf("non-path inputs collected as focusables: %d", len(m.focusables))
	}
}
