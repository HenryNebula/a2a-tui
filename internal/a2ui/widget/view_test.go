package widget

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// ---------------------------------------------------------------------------
// Rendering helpers
// ---------------------------------------------------------------------------

// buildSurface applies a CreateSurface through a real engine and returns the
// widget over it (for shapes the shared form fixture does not cover).
func buildSurface(t *testing.T, cs *a2ui.CreateSurface) *Model {
	t.Helper()
	eng := a2ui.NewEngine()
	raw, err := json.Marshal(map[string]any{"version": a2ui.VersionV1, "createSurface": cs})
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
	m, err := New(eng.Surface(cs.SurfaceID))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

// forceColorProfile pins lipgloss's default renderer to a color-capable
// profile for the duration of one test: test binaries run without a TTY,
// the detected profile is Ascii, and styles would render as plain text.
func forceColorProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI256)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// plainLines strips styling from a rendered view and splits it into lines.
func plainLines(view string) []string {
	return strings.Split(ansi.Strip(view), "\n")
}

// focusedLine returns the plain-text line of the focused widget (the one
// carrying the ▸ marker).
func focusedLine(t *testing.T, view string) string {
	t.Helper()
	for _, line := range plainLines(view) {
		if strings.HasPrefix(line, "▸") {
			return line
		}
	}
	t.Fatalf("no focused line in view:\n%s", view)
	return ""
}

// plainLine returns the first plain-text line containing want.
func plainLine(t *testing.T, view, want string) string {
	t.Helper()
	for _, line := range plainLines(view) {
		if strings.Contains(line, want) {
			return line
		}
	}
	t.Fatalf("no line containing %q in view:\n%s", want, view)
	return ""
}

// rawLine returns the first styled line whose plain text contains want.
func rawLine(view, want string) (string, bool) {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(ansi.Strip(line), want) {
			return line, true
		}
	}
	return "", false
}

// paddedLabel builds the expected padded label prefix of a stacked input
// line (label + colon, left-aligned into the label column).
func paddedLabel(label string) string {
	return label + ":" + strings.Repeat(" ", labelCol-1-ansi.StringWidth(label))
}

// bracketCol returns the cell column of the first "[" on a line (-1 when
// absent).
func bracketCol(line string) int {
	i := strings.Index(line, "[")
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(line[:i])
}

// interiorWidth returns the printable cell width between a line's brackets.
func interiorWidth(line string) int {
	open, close := strings.Index(line, "["), strings.LastIndex(line, "]")
	if open < 0 || close < 0 {
		return -1
	}
	return ansi.StringWidth(line[open+1 : close])
}

// ---------------------------------------------------------------------------
// Focus visibility
// ---------------------------------------------------------------------------

// TestFocusedFirstLineIsBold verifies the focused widget's own first line
// carries the focus styling (bold, accent marker) in addition to the ▸
// gutter marker, and that the styling follows focus.
func TestFocusedFirstLineIsBold(t *testing.T) {
	forceColorProfile(t)
	m, _ := buildForm(t, "focus-bold", false)
	line, ok := rawLine(m.View(80, 0), "Name:")
	if !ok || !strings.Contains(line, "\x1b[1m") || !strings.HasPrefix(ansi.Strip(line), "▸") {
		t.Fatalf("focused widget's first line is not bold:\n%s", m.View(80, 0))
	}
	if !strings.Contains(line, "\x1b[38;5;176m") {
		t.Fatalf("focused marker missing accent styling:\n%q", line)
	}
	tab(t, m, 2) // subscribe-check
	view := m.View(80, 0)
	if line, ok := rawLine(view, "Subscribe"); !ok || !strings.Contains(line, "\x1b[1m") {
		t.Fatalf("focused checkbox not bold:\n%s", view)
	}
	if line, ok := rawLine(view, "Name:"); ok && strings.Contains(line, "\x1b[1m") {
		t.Fatalf("unfocused field still bold:\n%s", view)
	}
}

// ---------------------------------------------------------------------------
// Bracket stability, uniform width, placeholder form
// ---------------------------------------------------------------------------

// TestFocusedTextFieldKeepsBrackets verifies the focused TextField renders
// its live editor inside the same enclosing brackets as the unfocused
// preview, so the field keeps a stable shape.
func TestFocusedTextFieldKeepsBrackets(t *testing.T) {
	m, _ := buildForm(t, "brackets", false)
	key(t, m, runes("Ada"))
	line := focusedLine(t, m.View(80, 0))
	if !strings.HasPrefix(line, "▸ Name:") || !strings.Contains(line, "[") || !strings.Contains(line, "]") {
		t.Fatalf("focused field lost its enclosing brackets:\n%s", m.View(80, 0))
	}
	tab(t, m, 1) // age-field, focused and empty (number hint follows the brackets)
	line = focusedLine(t, m.View(80, 0))
	if !strings.HasPrefix(line, "▸ Age:") || !strings.Contains(line, "[") || !strings.Contains(line, "]") {
		t.Fatalf("focused empty field lost its enclosing brackets:\n%s", m.View(80, 0))
	}
}

// TestFocusedDateTimeKeepsBrackets is the bracket-stability contract for the
// DateTimeInput editor.
func TestFocusedDateTimeKeepsBrackets(t *testing.T) {
	m, _ := buildForm(t, "dt-brackets", false)
	tab(t, m, 7) // meeting-at (the dim ISO hint follows the brackets)
	line := focusedLine(t, m.View(80, 0))
	if !strings.HasPrefix(line, "▸ Meeting:") ||
		!strings.Contains(line, "[2026-10-01T09:00") || !strings.Contains(line, "]") {
		t.Fatalf("focused date/time field lost its brackets:\n%s", m.View(80, 0))
	}
}

// TestFieldBracketWidthUniform verifies the closing bracket stays at the
// same column between focus states: the interior is padded to the fixed
// variant width whether the field shows its focused editor, a value, a
// declared placeholder, or a blank.
func TestFieldBracketWidthUniform(t *testing.T) {
	m, _ := buildForm(t, "uniform", false)
	key(t, m, runes("Ada"))
	focused := focusedLine(t, m.View(80, 0))
	tab(t, m, 1)
	view := m.View(80, 0)
	unfocused := plainLine(t, view, "Name:")
	want := fieldBodyW("shortText")
	for name, line := range map[string]string{"focused": focused, "unfocused": unfocused} {
		if w := interiorWidth(line); w != want {
			t.Fatalf("%s bracket interior = %d cells, want %d:\n%s", name, w, want, view)
		}
		if col := bracketCol(line); col != hintGutter+labelCol {
			t.Fatalf("%s bracket column = %d, want %d:\n%q", name, col, hintGutter+labelCol, line)
		}
	}
	// Empty without a declared placeholder: a blank interior of the
	// variant width (no synthetic word, no shrink-wrapping). Age is a
	// number field and is focused here; the focused editor renders the
	// same blank interior.
	age := "▸ " + paddedLabel("Age") + "[" + strings.Repeat(" ", numberBodyW) + "] 0-9"
	if got := plainLine(t, view, "Age:"); got != age {
		t.Fatalf("empty number field not blank at the fixed width:\n%q", got)
	}
}

// TestFieldWidthsByVariant verifies the bracket interior is tuned to the
// input kind: numbers are narrow, date/times medium, free text wide.
func TestFieldWidthsByVariant(t *testing.T) {
	m, _ := buildForm(t, "widths", false)
	view := m.View(80, 0)
	for label, want := range map[string]int{
		"Name":    fieldBodyW("shortText"), // focused editor
		"Age":     numberBodyW,             // unfocused, blank
		"Meeting": dateBodyW,               // unfocused, valued
	} {
		if w := interiorWidth(plainLine(t, view, label+":")); w != want {
			t.Fatalf("%s bracket interior = %d cells, want %d:\n%s", label, w, want, view)
		}
	}
}

// TestLabelColumnAlignment verifies stacked input labels are padded into a
// common label column, and labels longer than the column move to their own
// line above an input indented to the same column.
func TestLabelColumnAlignment(t *testing.T) {
	m, _ := buildForm(t, "labels", false)
	view := m.View(80, 0)
	wantCol := hintGutter + labelCol
	for _, label := range []string{"Name", "Age", "Meeting"} {
		if col := bracketCol(plainLine(t, view, label+":")); col != wantCol {
			t.Fatalf("%s brackets start at column %d, want %d:\n%s", label, col, wantCol, view)
		}
	}

	m2 := buildSurface(t, &a2ui.CreateSurface{
		SurfaceID: "longlabel",
		Components: []a2ui.Component{
			column("root", "when", "name"),
			{ID: "when", Component: "DateTimeInput", Props: &a2ui.DateTimeInputProps{
				Label: lit("Preferred meeting"), Value: bind("/when"),
				EnableDate: true, EnableTime: true,
			}},
			{ID: "name", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Name"), Value: bind("/name"),
			}},
		},
		DataModel: map[string]any{"when": "", "name": ""},
	})
	view2 := m2.View(80, 0)
	lines := plainLines(view2)
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "Preferred meeting:") {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(lines) {
		t.Fatalf("long label missing its own line:\n%s", view2)
	}
	// The wrapped case indents the input by a small fixed amount under its
	// label, not out at the label column.
	wantWrap := hintGutter + 2
	if col := bracketCol(lines[idx+1]); col != wantWrap {
		t.Fatalf("input below a long label indented to column %d, want %d:\n%s", col, wantWrap, view2)
	}
}

// TestPlaceholderRendersInsideBrackets verifies empty fields show their
// declared placeholder dim inside the brackets with no interior padding and
// no nested parens — and render blank when no placeholder was declared,
// both live and in read-only previews.
func TestPlaceholderRendersInsideBrackets(t *testing.T) {
	m, _ := buildForm(t, "placeholder", false)
	view := m.View(80, 0)
	if plain := ansi.Strip(view); strings.Contains(plain, "empty") || strings.Contains(plain, "[(]") {
		t.Fatalf("synthetic placeholder word or nested parens survived:\n%s", view)
	}

	m2 := buildSurface(t, &a2ui.CreateSurface{
		SurfaceID: "ph2",
		Components: []a2ui.Component{
			column("root", "first", "middle", "custom", "static"),
			{ID: "first", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("First"), Value: bind("/first"),
			}},
			{ID: "middle", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Middle"), Value: bind("/middle"),
			}},
			{ID: "custom", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Custom"), Value: bind("/custom"), Placeholder: lit("type here"),
			}},
			// Literal value: not bound to a writable path, so it renders as
			// a read-only preview.
			{ID: "static", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Static"), Value: lit(""), Placeholder: lit("type here"),
			}},
		},
		DataModel: map[string]any{"first": "", "middle": "", "custom": ""},
	})
	view2 := m2.View(80, 0)
	custom := plainLine(t, view2, "Custom:")
	if !strings.HasPrefix(custom, "  "+paddedLabel("Custom")) ||
		!strings.Contains(custom, "[type here") || !strings.HasSuffix(custom, "]") {
		t.Fatalf("custom placeholder not flush inside the brackets:\n%q", custom)
	}
	// Unfocused, empty, no declared placeholder: blank interior at the
	// fixed width.
	middle := "  " + paddedLabel("Middle") + "[" + strings.Repeat(" ", textBodyW) + "]"
	if got := plainLine(t, view2, "Middle:"); got != middle {
		t.Fatalf("empty unfocused field interior not blank at the fixed width:\n%q", got)
	}
	plain2 := ansi.Strip(view2)
	if !strings.Contains(plain2, "Static: [type here]") || strings.Contains(plain2, "Static: [ type here ") {
		t.Fatalf("read-only preview placeholder form wrong:\n%s", view2)
	}
}

// ---------------------------------------------------------------------------
// Hint gating and placement (no eager validation, no layout jitter)
// ---------------------------------------------------------------------------

// TestCheckHintsGatedUntilTouchedOrAttempted verifies a pristine surface
// renders no check-failure hints: they appear once the field's value was
// touched, or everywhere after a button activation was attempted.
func TestCheckHintsGatedUntilTouchedOrAttempted(t *testing.T) {
	m, _ := buildForm(t, "gate", false)
	if v := m.View(80, 0); strings.Contains(ansi.Strip(v), "Name is required") {
		t.Fatalf("pristine render shows check hints:\n%s", v)
	}
	key(t, m, runes("A"))
	key(t, m, press(tea.KeyBackspace))
	if v := m.View(80, 0); !strings.Contains(ansi.Strip(v), "Name is required") {
		t.Fatalf("touched field's check hint missing:\n%s", v)
	}
	// Without touching anything: a button activation attempt reveals the
	// failing checks.
	m2, _ := buildForm(t, "gate2", false)
	tab(t, m2, 8) // submit-btn (checks failing: /name empty)
	key(t, m2, press(tea.KeyEnter))
	if v := m2.View(80, 0); !strings.Contains(ansi.Strip(v), "Name is required") {
		t.Fatalf("hints stayed hidden after an activation attempt:\n%s", v)
	}
}

// TestCheckHintInlinePlacement verifies a failing check renders to the right
// of its field's line when it fits (nothing below jumps when the check
// flips) and falls back to a line below when it does not fit.
func TestCheckHintInlinePlacement(t *testing.T) {
	m, _ := buildForm(t, "hints", false)
	key(t, m, runes("A"))
	key(t, m, press(tea.KeyBackspace)) // touched, empty → check fails
	view := m.View(80, 0)
	line, ok := rawLine(view, "Name:")
	if !ok || !strings.Contains(ansi.Strip(line), "Name is required") {
		t.Fatalf("check hint not inlined beside the field:\n%s", view)
	}
	narrow := m.View(30, 0)
	below := false
	for _, l := range plainLines(narrow) {
		hasField := strings.Contains(l, "Name:") && strings.Contains(l, "[")
		hasHint := strings.Contains(l, "Name is required")
		if hasField && hasHint {
			t.Fatalf("hint inlined past the available width:\n%s", narrow)
		}
		if hasHint && !hasField {
			below = true
		}
	}
	if !below {
		t.Fatalf("hint missing from the narrow view:\n%s", narrow)
	}
}

// ---------------------------------------------------------------------------
// Button affordance
// ---------------------------------------------------------------------------

// TestButtonStyling verifies the button's visual states: failing checks dim
// (with the disabled marker), unfocused secondary bold, focused bold plus
// reverse video.
func TestButtonStyling(t *testing.T) {
	forceColorProfile(t)
	m, _ := buildForm(t, "button-style", false)
	line, ok := rawLine(m.View(80, 0), "[ Submit ]")
	if !ok {
		t.Fatalf("submit button missing:\n%s", m.View(80, 0))
	}
	if !strings.Contains(ansi.Strip(line), "(disabled)") || strings.Contains(line, "\x1b[1m") {
		t.Fatalf("disabled button must stay dim:\n%s", m.View(80, 0))
	}
	key(t, m, runes("Ada")) // /name filled → the button's checks pass
	view := m.View(80, 0)
	if line, ok := rawLine(view, "[ Submit ]"); !ok || !strings.Contains(line, "\x1b[1m") ||
		strings.Contains(ansi.Strip(line), "(disabled)") {
		t.Fatalf("unfocused secondary button not bold:\n%s", view)
	}
	tab(t, m, 8)
	view = m.View(80, 0)
	if line, ok := rawLine(view, "[ Submit ]"); !ok || !strings.Contains(line, "\x1b[1;7m") {
		t.Fatalf("focused button missing bold+reverse:\n%s", view)
	}
}

// TestPrimaryButtonAccent verifies unfocused primary buttons carry the
// accent color so actions stand out from the fields.
func TestPrimaryButtonAccent(t *testing.T) {
	forceColorProfile(t)
	m := buildSurface(t, &a2ui.CreateSurface{
		SurfaceID: "btnvar",
		Components: []a2ui.Component{
			column("root", "f", "pb", "pb-label", "db", "db-label"),
			{ID: "f", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Name"), Value: bind("/name"),
			}},
			{ID: "pb", Component: "Button", Props: &a2ui.ButtonProps{
				Child: "pb-label", Variant: "primary",
			}},
			textComponent("pb-label", "Save", "body"),
			{ID: "db", Component: "Button", Props: &a2ui.ButtonProps{Child: "db-label"}},
			textComponent("db-label", "Cancel", "body"),
		},
		DataModel: map[string]any{"name": ""},
	})
	view := m.View(80, 0)
	if line, ok := rawLine(view, "[[ Save ]]"); !ok || !strings.Contains(line, "\x1b[38;5;176m") {
		t.Fatalf("primary button missing accent styling:\n%s", view)
	}
	if line, ok := rawLine(view, "[ Cancel ]"); !ok || !strings.Contains(line, "\x1b[1m") {
		t.Fatalf("secondary button missing bold:\n%s", view)
	}
}

// TestButtonGroupSeparatedByBlankLine verifies one blank line separates a
// button (group) from the content above it, with no blank between adjacent
// buttons.
func TestButtonGroupSeparatedByBlankLine(t *testing.T) {
	buildButtons := func(id string) *a2ui.CreateSurface {
		return &a2ui.CreateSurface{
			SurfaceID: id,
			Components: []a2ui.Component{
				column("root", "f", "pb", "pb-label", "db", "db-label"),
				{ID: "f", Component: "TextField", Props: &a2ui.TextFieldProps{
					Label: lit("Name"), Value: bind("/name"),
				}},
				{ID: "pb", Component: "Button", Props: &a2ui.ButtonProps{Child: "pb-label"}},
				textComponent("pb-label", "Save", "body"),
				{ID: "db", Component: "Button", Props: &a2ui.ButtonProps{Child: "db-label"}},
				textComponent("db-label", "Cancel", "body"),
			},
			DataModel: map[string]any{"name": ""},
		}
	}
	m, _ := buildForm(t, "btnsep", false)
	lines := plainLines(m.View(80, 0))
	idx := -1
	for i, l := range lines {
		if strings.Contains(l, "[ Submit ]") {
			idx = i
			break
		}
	}
	if idx <= 0 || lines[idx-1] != "" {
		t.Fatalf("button not separated from the fields by a blank line:\n%s", m.View(80, 0))
	}

	m2 := buildSurface(t, buildButtons("btngrp"))
	lines = plainLines(m2.View(80, 0))
	save, cancel := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "[ Save ]") {
			save = i
		}
		if strings.Contains(l, "[ Cancel ]") {
			cancel = i
		}
	}
	if save <= 0 || lines[save-1] != "" {
		t.Fatalf("button group not preceded by a blank line:\n%s", m2.View(80, 0))
	}
	if cancel == save+1 && lines[cancel-1] == "" {
		t.Fatalf("blank line inside the button group:\n%s", m2.View(80, 0))
	}
}

// ---------------------------------------------------------------------------
// Slider affordance
// ---------------------------------------------------------------------------

// TestSliderHandleGlyph verifies the slider track carries its handle glyph
// at the fill boundary — including index 0 at zero — keeps the value/max
// text and the focused stepping hint, and that the handle renders as an
// accent thumb (no reverse block) on the track.
func TestSliderHandleGlyph(t *testing.T) {
	forceColorProfile(t)
	m, _ := buildForm(t, "slider-handle", false)
	if line, ok := rawLine(m.View(80, 0), "[█████●░░░░]"); ok && strings.Contains(line, ";7m") {
		t.Fatalf("slider handle rendered with reverse video:\n%q", line)
	}
	plain := ansi.Strip(m.View(80, 0))
	if !strings.Contains(plain, "Volume: 7/11 [█████●░░░░]") {
		t.Fatalf("slider handle missing at the fill boundary:\n%s", plain)
	}
	if !strings.Contains(plain, "Plain: 50/100 [████●░░░░░]") {
		t.Fatalf("mid-range slider handle missing:\n%s", plain)
	}
	tab(t, m, 5) // volume-slider
	plain = ansi.Strip(m.View(80, 0))
	if !strings.Contains(plain, "Volume: 7/11 [█████●░░░░]") || !strings.Contains(plain, "←/→ step") {
		t.Fatalf("focused slider lost its bar or stepping hint:\n%s", plain)
	}
	m2 := buildSurface(t, &a2ui.CreateSurface{
		SurfaceID: "slide0",
		Components: []a2ui.Component{
			column("root", "s"),
			{ID: "s", Component: "Slider", Props: &a2ui.SliderProps{
				Label: lit("Zero"), Min: f64(0), Max: f64(10), Value: bind("/v"),
			}},
		},
		DataModel: map[string]any{"v": 0},
	})
	if plain2 := ansi.Strip(m2.View(80, 0)); !strings.Contains(plain2, "[●░░░░░░░░░]") {
		t.Fatalf("zero-value slider should show the handle at index 0:\n%s", plain2)
	}
}

// ---------------------------------------------------------------------------
// Editor cursor
// ---------------------------------------------------------------------------

// TestEditorCursorStyled verifies the embedded editors carry an explicit
// contrast cursor style (reverse block in the accent color), so the
// insertion point stays visible on terminals where a bare reverse space
// disappears.
func TestEditorCursorStyled(t *testing.T) {
	m, _ := buildForm(t, "cursor", false)
	for _, f := range m.focusables {
		if !f.usesInput() {
			continue
		}
		st := f.input.Cursor.Style
		if !st.GetReverse() ||
			st.GetForeground() != lipgloss.Color("231") ||
			st.GetBackground() != lipgloss.Color("176") {
			t.Fatalf("editor of %s lacks explicit cursor contrast style", f.id)
		}
	}
}

// ---------------------------------------------------------------------------
// longText editor note
// ---------------------------------------------------------------------------

// TestLongTextNoteIsBrief verifies the focused longText field hints at its
// single-line editor without leaking the internal rendering note.
func TestLongTextNoteIsBrief(t *testing.T) {
	m := buildSurface(t, &a2ui.CreateSurface{
		SurfaceID: "long",
		Components: []a2ui.Component{
			column("root", "bio"),
			{ID: "bio", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Bio"), Value: bind("/bio"), Variant: "longText",
			}},
		},
		DataModel: map[string]any{"bio": ""},
	})
	view := m.View(80, 0)
	if !strings.Contains(ansi.Strip(view), "(single-line editor)") {
		t.Fatalf("longText editor note missing:\n%s", view)
	}
	if strings.Contains(view, "one-line editor") || strings.Contains(view, "wraps in previews") {
		t.Fatalf("internal longText note leaked:\n%s", view)
	}
}

// ---------------------------------------------------------------------------
// Static text gutter
// ---------------------------------------------------------------------------

// TestStaticTextLinesUpWithGutter verifies static text and read-only
// previews indent by the 2-column focus gutter, so the surface's left edge
// is straight.
func TestStaticTextLinesUpWithGutter(t *testing.T) {
	m, _ := buildForm(t, "gutter", false)
	view := m.View(80, 0)
	if title := plainLine(t, view, "Contact form"); !strings.HasPrefix(title, "  ") {
		t.Fatalf("static text not indented by the focus gutter:\n%q", title)
	}
	m2 := buildSurface(t, &a2ui.CreateSurface{
		SurfaceID: "gut2",
		Components: []a2ui.Component{
			column("root", "t", "ro"),
			textComponent("t", "Read only", "body"),
			{ID: "ro", Component: "TextField", Props: &a2ui.TextFieldProps{
				Label: lit("Literal"), Value: lit("fixed"),
			}},
		},
	})
	view2 := m2.View(80, 0)
	if line := plainLine(t, view2, "Read only"); !strings.HasPrefix(line, "  ") {
		t.Fatalf("static text not indented:\n%q", line)
	}
	if line := plainLine(t, view2, "Literal: [fixed]"); !strings.HasPrefix(line, "  ") {
		t.Fatalf("read-only preview not indented:\n%q", line)
	}
}
