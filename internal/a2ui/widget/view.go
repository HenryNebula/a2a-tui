package widget

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/render"
)

// View rendering limits.
const (
	// maxViewLines bounds the rendered surface (the pane's viewport
	// scrolls within this budget).
	maxViewLines = 400
	// sliderCells is the slider bar width in cells.
	sliderCells = 10
)

// View styles. The surface content is untrusted: every evaluated string is
// sanitized (ANSI + control stripped) before styling, and lines are
// truncated to the given width.
var (
	styleFocus    = lipgloss.NewStyle().Bold(true).Reverse(true)
	styleFocused  = lipgloss.NewStyle().Bold(true)
	styleHint     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleDimW     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleValue    = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleHeading  = lipgloss.NewStyle().Bold(true)
	styleDisabled = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// View renders the full interactive surface: static subtrees as compact
// previews and focusables with their live editors and focus indicators.
// height <= 0 means no height clip (the TUI pane scrolls the output).
func (m *Model) View(width, height int) string {
	if width < 1 {
		width = 1
	}
	if m.root == nil {
		return styleDimW.Render("(surface has no root component yet)")
	}
	r := &viewRenderer{m: m, width: width, byID: map[string]*focusable{}}
	for _, f := range m.focusables {
		r.byID[f.id] = f
	}
	out := r.node(m.root)
	lines := strings.Split(out, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "…")
	}
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	} else if len(lines) > maxViewLines {
		kept := lines[:maxViewLines-1]
		lines = append(kept, styleDimW.Render("…"))
	}
	return strings.Join(lines, "\n")
}

// viewRenderer renders one pass over the materialized tree.
type viewRenderer struct {
	m     *Model
	width int
	byID  map[string]*focusable
}

// node renders one tree node.
func (r *viewRenderer) node(n *a2ui.Node) string {
	if n == nil {
		return ""
	}
	if n.Placeholder || n.Component == nil {
		return styleDimW.Render("┄ unresolved " + clean(n.RefID))
	}
	if f, ok := r.byID[n.Component.ID]; ok && f.node == n {
		return r.focusable(f)
	}
	ctx := n.EvalContext(r.m.evalCtx())
	switch props := n.Component.Props.(type) {
	case *a2ui.TextProps:
		content := clean(render.StripMarkdown(props.Text.EvalString(ctx)))
		style := lipgloss.NewStyle()
		switch props.Variant {
		case "h1", "h2", "h3":
			style = styleHeading
		case "h4", "h5":
			style = lipgloss.NewStyle().Underline(true)
		}
		return wrapStatic(style.Render(content), r.width)
	case *a2ui.ImageProps:
		return wrapStatic("[image "+clean(props.URL.EvalString(ctx))+"]", r.width)
	case *a2ui.VideoProps:
		return wrapStatic("[video "+clean(props.URL.EvalString(ctx))+"]", r.width)
	case *a2ui.AudioPlayerProps:
		return wrapStatic("[audio "+clean(props.URL.EvalString(ctx))+"]", r.width)
	case *a2ui.IconProps:
		return r.icon(props, ctx)
	case *a2ui.DividerProps:
		if props.Axis == "vertical" {
			return "│"
		}
		return strings.Repeat("─", maxIntW(1, r.width))
	case *a2ui.RowProps:
		return r.row(n)
	case *a2ui.ColumnProps:
		return r.stack(n)
	case *a2ui.ListProps:
		if props.Direction == "horizontal" {
			var parts []string
			for _, c := range n.Children {
				parts = append(parts, r.node(c))
			}
			return strings.Join(parts, " ")
		}
		return r.stack(n)
	case *a2ui.CardProps:
		inner := ""
		if len(n.Children) == 1 {
			inner = r.at(n.Children[0], maxIntW(1, r.width-4))
		}
		if strings.TrimSpace(inner) == "" {
			inner = " "
		}
		return lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			Padding(0, 1).
			MaxWidth(r.width).
			Render(inner)
	case *a2ui.TabsProps:
		var titles []string
		for i, t := range props.Tabs {
			title := clean(t.Title.EvalString(ctx))
			if title == "" {
				title = "tab"
			}
			if i == 0 {
				titles = append(titles, "["+title+"]")
			} else {
				titles = append(titles, title)
			}
		}
		bar := ansi.Truncate(strings.Join(titles, " | "), r.width, "…")
		if len(n.Children) > 0 {
			return bar + "\n" + r.node(n.Children[0])
		}
		return bar
	case *a2ui.ModalProps:
		var parts []string
		if len(n.Children) > 0 {
			parts = append(parts, r.node(n.Children[0]))
		}
		if len(n.Children) > 1 {
			parts = append(parts, styleDimW.Render("┄┄ modal ┄┄"))
			for _, line := range strings.Split(r.node(n.Children[1]), "\n") {
				parts = append(parts, "  "+line)
			}
		}
		return strings.Join(parts, "\n")
	case *a2ui.UnknownProps:
		return styleDimW.Render(ansi.Truncate(" unsupported component "+clean(n.Component.Component), r.width, "…"))
	default:
		return styleDimW.Render(ansi.Truncate(" unsupported component "+clean(n.Component.Component), r.width, "…"))
	}
}

// icon renders an Icon node.
func (r *viewRenderer) icon(props *a2ui.IconProps, ctx a2ui.EvalContext) string {
	switch props.Name.Kind {
	case a2ui.IconKindEnum:
		return render.Glyph(props.Name.Enum)
	case a2ui.IconKindSVG:
		return "◆ " + clean(props.Name.SVGPath.EvalString(ctx))
	default:
		name := props.Name.Dynamic.EvalString(ctx)
		if name == "" {
			return render.Glyph("")
		}
		return render.Glyph(name)
	}
}

// row renders children side by side, sharing the width equally.
func (r *viewRenderer) row(n *a2ui.Node) string {
	if len(n.Children) == 0 {
		return ""
	}
	w := maxIntW(4, r.width/len(n.Children))
	blocks := make([]string, len(n.Children))
	for i, c := range n.Children {
		blocks[i] = lipgloss.NewStyle().Width(w).MaxWidth(w).Render(r.at(c, w))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

// stack renders children vertically.
func (r *viewRenderer) stack(n *a2ui.Node) string {
	var lines []string
	for _, c := range n.Children {
		lines = append(lines, r.node(c))
	}
	return strings.Join(lines, "\n")
}

// at renders a child constrained to w columns.
func (r *viewRenderer) at(n *a2ui.Node, w int) string {
	saved := r.width
	r.width = w
	out := r.node(n)
	r.width = saved
	return out
}

// ---------------------------------------------------------------------------
// Focusable components
// ---------------------------------------------------------------------------

// focusable renders one interactive component with its focus indicator and
// validation hints.
func (r *viewRenderer) focusable(f *focusable) string {
	focused := r.m.current() == f
	marker := "  "
	if focused {
		marker = "▸ "
	}
	var lines []string
	switch f.kind {
	case kindButton:
		lines = r.buttonLines(f, focused)
	case kindTextField:
		lines = r.textFieldLines(f, focused)
	case kindCheckBox:
		lines = r.checkBoxLines(f)
	case kindChoicePicker:
		lines = r.pickerLines(f, focused)
	case kindSlider:
		lines = r.sliderLines(f, focused)
	case kindDateTime:
		lines = r.dateTimeLines(f, focused)
	}
	if len(lines) == 0 {
		return ""
	}
	lines[0] = marker + lines[0]
	for i := 1; i < len(lines); i++ {
		lines[i] = "  " + lines[i]
	}
	if focused && r.m.flash != "" {
		lines = append(lines, styleHint.Render("  ✗ "+clean(r.m.flash)))
	}
	return strings.Join(lines, "\n")
}

// buttonLines renders a Button; failing checks mark it disabled.
func (r *viewRenderer) buttonLines(f *focusable, focused bool) []string {
	props, ok := f.node.Component.Props.(*a2ui.ButtonProps)
	if !ok {
		return nil
	}
	label := ""
	if len(f.node.Children) > 0 {
		label = strings.Join(strings.Split(r.node(f.node.Children[0]), "\n"), " ")
	}
	label = clean(strings.TrimSpace(label))
	var line string
	switch props.Variant {
	case "primary":
		line = "[[ " + label + " ]]"
	case "borderless":
		line = label
	default:
		line = "[ " + label + " ]"
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	disabled := false
	var hints []string
	for _, rule := range props.Checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			disabled = true
			hints = append(hints, styleHint.Render("✗ "+checkMessage(res, rule)))
		}
	}
	if disabled {
		line += styleDisabled.Render(" (disabled)")
	} else if focused {
		line = styleFocus.Render(line)
	} else if props.Variant == "primary" {
		line = styleFocused.Render(line)
	}
	out := []string{line}
	return append(out, hints...)
}

// textFieldLines renders a TextField with its live editor. longText fields
// are edited on one widened line (documented simplification).
func (r *viewRenderer) textFieldLines(f *focusable, focused bool) []string {
	props, ok := f.node.Component.Props.(*a2ui.TextFieldProps)
	if !ok {
		return nil
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	label := clean(props.Label.EvalString(ctx))
	var body string
	if focused {
		body = f.input.View()
	} else {
		shown := clean(f.input.Value())
		if shown == "" {
			shown = styleDimW.Render("(" + clean(firstNonEmptyStr(props.Placeholder.EvalString(ctx), "empty")) + ")")
		} else if props.Variant == "obscured" {
			shown = strings.Repeat("•", len([]rune(shown)))
		}
		body = "[" + shown + "]"
	}
	if props.Variant == "number" && focused {
		body += styleDimW.Render(" 0-9")
	}
	line := label + ": " + body
	if focused && props.Variant == "longText" {
		line += styleDimW.Render("  (long text — one-line editor, wraps in previews)")
	}
	out := []string{line}
	// Widget-local format validation for number variants.
	if props.Variant == "number" {
		if raw := strings.TrimSpace(f.input.Value()); raw != "" {
			if _, err := parseNumber(raw); err != nil {
				out = append(out, styleHint.Render("✗ must be a number"))
			}
		}
	}
	out = append(out, r.checkHints(props.Checks, ctx)...)
	return out
}

// checkBoxLines renders a CheckBox.
func (r *viewRenderer) checkBoxLines(f *focusable) []string {
	props, ok := f.node.Component.Props.(*a2ui.CheckBoxProps)
	if !ok {
		return nil
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	mark := "[ ]"
	if props.Value.EvalBoolean(ctx) {
		mark = "[x]"
	}
	out := []string{mark + " " + clean(props.Label.EvalString(ctx))}
	return append(out, r.checkHints(props.Checks, ctx)...)
}

// pickerLines renders a ChoicePicker: label, optional filter line, and the
// options with selection markers and a cursor on the focused picker.
func (r *viewRenderer) pickerLines(f *focusable, focused bool) []string {
	props := f.picker()
	if props == nil {
		return nil
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	selected := map[string]bool{}
	for _, v := range props.Value.EvalStringList(ctx) {
		selected[v] = true
	}
	var out []string
	if label := clean(props.Label.EvalString(ctx)); label != "" {
		out = append(out, label+":")
	}
	if props.Filterable && focused {
		out = append(out, "filter: "+f.input.View())
	}
	on, off := "(•)", "( )"
	if props.Variant == "multipleSelection" {
		on, off = "[x]", "[ ]"
	}
	for i, opt := range f.visibleOptions(r.m) {
		marker := off
		if selected[opt.Value] {
			marker = on
		}
		cursor := "  "
		if focused && i == f.cursor {
			cursor = "❯ "
		}
		out = append(out, cursor+marker+" "+clean(opt.Label.EvalString(ctx)))
	}
	if len(out) == 1 && len(f.visibleOptions(r.m)) == 0 {
		out = append(out, styleDimW.Render("  (no matching options)"))
	}
	return append(out, r.checkHints(props.Checks, ctx)...)
}

// sliderLines renders a Slider with its bar and the stepping hint.
func (r *viewRenderer) sliderLines(f *focusable, focused bool) []string {
	props, ok := f.node.Component.Props.(*a2ui.SliderProps)
	if !ok {
		return nil
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	label := clean(props.Label.EvalString(ctx))
	value := props.Value.EvalNumber(ctx)
	min, max := 0.0, 100.0
	if props.Min != nil {
		min = *props.Min
	}
	if props.Max != nil {
		max = *props.Max
	}
	if max <= min {
		max = min + 1
	}
	filled := int((value - min) / (max - min) * float64(sliderCells))
	if filled < 0 {
		filled = 0
	}
	if filled > sliderCells {
		filled = sliderCells
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", sliderCells-filled)
	line := fmt.Sprintf("%s: %s/%s [%s]", label,
		a2ui.Stringify(value), a2ui.Stringify(max), bar)
	if focused {
		line += styleDimW.Render("  ←/→ step · ↑/↓ 10%")
	}
	out := []string{line}
	return append(out, r.checkHints(props.Checks, ctx)...)
}

// dateTimeLines renders a DateTimeInput with its live ISO 8601 editor and
// format/range hints.
func (r *viewRenderer) dateTimeLines(f *focusable, focused bool) []string {
	props, ok := f.node.Component.Props.(*a2ui.DateTimeInputProps)
	if !ok {
		return nil
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	label := clean(props.Label.EvalString(ctx))
	if label != "" {
		label += ": "
	}
	var body string
	if focused {
		body = f.input.View()
	} else {
		shown := clean(f.input.Value())
		if shown == "" {
			shown = styleDimW.Render("(" + dateTimePlaceholder(props.EnableDate, props.EnableTime) + ")")
		}
		body = "[" + shown + "]"
	}
	out := []string{label + body + styleDimW.Render("  ISO 8601")}
	if raw := strings.TrimSpace(f.input.Value()); raw != "" {
		if ts := parseDateTime(raw); ts == nil {
			out = append(out, styleHint.Render("✗ invalid date/time (want "+dateTimePlaceholder(props.EnableDate, props.EnableTime)+")"))
		} else {
			if minRaw := strings.TrimSpace(props.Min.EvalString(ctx)); minRaw != "" {
				if min := parseDateTime(minRaw); min != nil && ts.Before(*min) {
					out = append(out, styleHint.Render("✗ must be after "+minRaw))
				}
			}
			if maxRaw := strings.TrimSpace(props.Max.EvalString(ctx)); maxRaw != "" {
				if max := parseDateTime(maxRaw); max != nil && ts.After(*max) {
					out = append(out, styleHint.Render("✗ must be before "+maxRaw))
				}
			}
		}
	}
	return append(out, r.checkHints(props.Checks, ctx)...)
}

// checkHints renders the failing agent-defined checks of a component.
func (r *viewRenderer) checkHints(checks []a2ui.CheckRule, ctx a2ui.EvalContext) []string {
	var out []string
	for _, rule := range checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			out = append(out, styleHint.Render("✗ "+checkMessage(res, rule)))
		}
	}
	return out
}

// checkMessage picks the display message for a failing check.
func checkMessage(res a2ui.ValidationResult, rule a2ui.CheckRule) string {
	msg := a2ui.CheckDisplayMessage(res, rule)
	if msg == "" {
		return "invalid"
	}
	return clean(msg)
}

// ---------------------------------------------------------------------------
// Sanitizing helpers
// ---------------------------------------------------------------------------

// clean strips ANSI escapes and control characters from an evaluated
// (untrusted) string, keeping it single-line.
func clean(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("  ")
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			// drop C0/C1 controls (newlines included: one line per value)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// wrapStatic hard-wraps a sanitized static line to width.
func wrapStatic(s string, width int) string {
	if s == "" {
		return ""
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, ansi.Hardwrap(line, width, true))
	}
	return strings.Join(out, "\n")
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func maxIntW(a, b int) int {
	if a > b {
		return a
	}
	return b
}
