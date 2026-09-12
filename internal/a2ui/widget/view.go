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
	// hintGap is the gap between a widget's line and a hint appended to
	// its right.
	hintGap = 2
	// hintGutter is the width of the focus marker (and continuation
	// prefix) focusable prepends to every line.
	hintGutter = 2
	// hintMargin keeps inline hints this far from the right edge.
	hintMargin = 2
	// labelCol is the fixed label column of stacked inputs: labels are
	// left-aligned and padded to it so every input's brackets start at the
	// same column; longer labels get a line of their own above the input.
	labelCol = 18
	// Bracket interior widths per input kind (both focus states): the
	// embedded editor renders Width cells plus one cursor cell, and the
	// unfocused interior is padded to the same width, so brackets never
	// move on focus changes. Numbers and ISO 8601 date/times need far
	// fewer cells than free text.
	textBodyW   = inputWidth + 1 // shortText, longText, obscured
	numberBodyW = 8
	dateBodyW   = 22
)

// fieldBodyW returns the fixed bracket-interior width of a TextField
// variant (the DateTimeInput editor uses dateBodyW).
func fieldBodyW(variant string) int {
	if variant == "number" {
		return numberBodyW
	}
	return textBodyW
}

// View styles. The surface content is untrusted: every evaluated string is
// sanitized (ANSI + control stripped) before styling, and lines are
// truncated to the given width.
var (
	styleFocus    = lipgloss.NewStyle().Bold(true).Reverse(true)
	styleFocused  = lipgloss.NewStyle().Bold(true)
	styleAccent   = lipgloss.NewStyle().Foreground(lipgloss.Color("176"))
	styleHandle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("176"))
	styleHint     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleDimW     = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
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
	r := &viewRenderer{m: m, width: width, byNode: make(map[*a2ui.Node]*focusable, len(m.focusables))}
	for _, f := range m.focusables {
		r.byNode[f.node] = f
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
	// byNode maps the materialized node each focusable was collected from;
	// template rows instantiate distinct nodes, so this identifies the
	// editable occurrence precisely.
	byNode map[*a2ui.Node]*focusable
}

// node renders one tree node.
func (r *viewRenderer) node(n *a2ui.Node) string {
	if n == nil {
		return ""
	}
	if n.Placeholder || n.Component == nil {
		return gutterStatic(styleDimW.Render("┄ unresolved " + clean(n.RefID)))
	}
	if f, ok := r.byNode[n]; ok {
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
		return gutterStatic(wrapStatic(style.Render(content), maxIntW(1, r.width-hintGutter)))
	case *a2ui.ImageProps:
		return gutterStatic(wrapStatic("[image "+clean(props.URL.EvalString(ctx))+"]", maxIntW(1, r.width-hintGutter)))
	case *a2ui.VideoProps:
		return gutterStatic(wrapStatic("[video "+clean(props.URL.EvalString(ctx))+"]", maxIntW(1, r.width-hintGutter)))
	case *a2ui.AudioPlayerProps:
		return gutterStatic(wrapStatic("[audio "+clean(props.URL.EvalString(ctx))+"]", maxIntW(1, r.width-hintGutter)))
	case *a2ui.IconProps:
		return gutterStatic(r.icon(props, ctx))
	case *a2ui.DividerProps:
		if props.Axis == "vertical" {
			return gutterStatic("│")
		}
		return gutterStatic(strings.Repeat("─", maxIntW(1, r.width-hintGutter)))
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
		return gutterStatic(styleDimW.Render(ansi.Truncate(" unsupported component "+clean(n.Component.Component), maxIntW(1, r.width-hintGutter), "…")))
	case *a2ui.ButtonProps:
		return r.staticButton(n, props, ctx)
	case *a2ui.TextFieldProps:
		return r.staticTextField(props, ctx)
	case *a2ui.CheckBoxProps:
		return r.staticCheckBox(props, ctx)
	case *a2ui.ChoicePickerProps:
		return r.staticPicker(props, ctx)
	case *a2ui.SliderProps:
		return r.staticSlider(props, ctx)
	case *a2ui.DateTimeInputProps:
		return r.staticDateTime(props, ctx)
	default:
		return gutterStatic(styleDimW.Render(ansi.Truncate(" unsupported component "+clean(n.Component.Component), maxIntW(1, r.width-hintGutter), "…")))
	}
}

// gutterStatic indents static (non-interactive) lines by the 2-column
// focus gutter, so form text lines up with the interactive widgets below
// it instead of hanging into the marker column.
func gutterStatic(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = strings.Repeat(" ", hintGutter) + line
		}
	}
	return strings.Join(lines, "\n")
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

// stack renders children vertically, inserting one blank line before a
// button (group) that follows non-button content, so actions are visually
// separated from the fields above them.
func (r *viewRenderer) stack(n *a2ui.Node) string {
	var lines []string
	for i, c := range n.Children {
		if i > 0 && isButtonNode(c) && !isButtonNode(n.Children[i-1]) {
			lines = append(lines, "")
		}
		lines = append(lines, r.node(c))
	}
	return strings.Join(lines, "\n")
}

// isButtonNode reports whether a node renders a Button (the focusable
// editor or the read-only preview).
func isButtonNode(n *a2ui.Node) bool {
	return n != nil && !n.Placeholder && n.Component != nil && n.Component.Component == "Button"
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
// Read-only input previews
// ---------------------------------------------------------------------------

// readOnly dims one preview line: the value previews of inputs that hold
// no focusable editor — read-only values (not bound to a writable path)
// and later references of an already-collected component (shared DAG
// subtrees) — mirroring the static renderer's display in the widget's
// compact style.
func readOnly(s string) string {
	if s == "" {
		return ""
	}
	return styleDisabled.Render(s)
}

// staticButton renders a non-editable Button as a dim preview; failing
// checks mark it disabled like the focusable button does.
func (r *viewRenderer) staticButton(n *a2ui.Node, props *a2ui.ButtonProps, ctx a2ui.EvalContext) string {
	label := ""
	if len(n.Children) > 0 {
		label = strings.Join(strings.Split(r.node(n.Children[0]), "\n"), " ")
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
	disabled := false
	for _, rule := range props.Checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			disabled = true
			break
		}
	}
	if disabled {
		line += " (disabled)"
	}
	return gutterStatic(joinPreview([]string{readOnly(line)}, r.checkHints(props.Checks, ctx, r.showChecks(nil))))
}

// staticTextField renders a read-only TextField preview: the value, the
// declared placeholder, or an empty bracket pair (never a synthetic word).
func (r *viewRenderer) staticTextField(props *a2ui.TextFieldProps, ctx a2ui.EvalContext) string {
	shown := clean(props.Value.EvalString(ctx))
	switch {
	case shown == "":
		shown = clean(props.Placeholder.EvalString(ctx))
	case props.Variant == "obscured":
		shown = strings.Repeat("•", len([]rune(shown)))
	}
	return gutterStatic(joinPreview([]string{readOnly(clean(props.Label.EvalString(ctx)) + ": [" + shown + "]")},
		r.checkHints(props.Checks, ctx, r.showChecks(nil))))
}

// staticCheckBox renders a read-only CheckBox preview.
func (r *viewRenderer) staticCheckBox(props *a2ui.CheckBoxProps, ctx a2ui.EvalContext) string {
	mark := "[ ]"
	if props.Value.EvalBoolean(ctx) {
		mark = "[x]"
	}
	return gutterStatic(joinPreview([]string{readOnly(mark + " " + clean(props.Label.EvalString(ctx)))},
		r.checkHints(props.Checks, ctx, r.showChecks(nil))))
}

// staticPicker renders a read-only ChoicePicker preview: label plus the
// options with their selection markers.
func (r *viewRenderer) staticPicker(props *a2ui.ChoicePickerProps, ctx a2ui.EvalContext) string {
	selected := map[string]bool{}
	for _, v := range props.Value.EvalStringList(ctx) {
		selected[v] = true
	}
	on, off := "(•)", "( )"
	if props.Variant == "multipleSelection" {
		on, off = "[x]", "[ ]"
	}
	var lines []string
	if label := clean(props.Label.EvalString(ctx)); label != "" {
		lines = append(lines, readOnly(label+":"))
	}
	for _, opt := range props.Options {
		marker := off
		if selected[opt.Value] {
			marker = on
		}
		lines = append(lines, readOnly("  "+marker+" "+clean(opt.Label.EvalString(ctx))))
	}
	if len(lines) == 0 {
		return ""
	}
	return gutterStatic(joinPreview(lines, r.checkHints(props.Checks, ctx, r.showChecks(nil))))
}

// staticSlider renders a read-only Slider preview with its bar.
func (r *viewRenderer) staticSlider(props *a2ui.SliderProps, ctx a2ui.EvalContext) string {
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
	return gutterStatic(joinPreview([]string{readOnly(line)}, r.checkHints(props.Checks, ctx, r.showChecks(nil))))
}

// staticDateTime renders a read-only DateTimeInput preview.
func (r *viewRenderer) staticDateTime(props *a2ui.DateTimeInputProps, ctx a2ui.EvalContext) string {
	shown := clean(props.Value.EvalString(ctx))
	if shown == "" {
		shown = dateTimePlaceholder(props.EnableDate, props.EnableTime)
	}
	label := clean(props.Label.EvalString(ctx))
	if label != "" {
		label += ": "
	}
	return gutterStatic(joinPreview([]string{readOnly(label + "[" + shown + "]")},
		r.checkHints(props.Checks, ctx, r.showChecks(nil))))
}

// joinPreview joins dimmed preview lines with their (undimmed) check hints.
func joinPreview(lines, hints []string) string {
	return strings.Join(append(lines, hints...), "\n")
}

// ---------------------------------------------------------------------------
// Focusable components
// ---------------------------------------------------------------------------

// focusable renders one interactive component with its focus indicator and
// validation hints. The focused widget is marked several ways: the accent
// ▸ gutter marker, bold styling of its first line (label + value), and
// accent brackets on the focused inputs, so focus stays unmistakable.
func (r *viewRenderer) focusable(f *focusable) string {
	focused := r.m.current() == f
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
	if focused {
		lines[0] = styleFocused.Render(styleAccent.Render("▸ ") + lines[0])
	} else {
		lines[0] = "  " + lines[0]
	}
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
			if r.showChecks(f) {
				hints = append(hints, styleHint.Render("✗ "+checkMessage(res, rule)))
			}
		}
	}
	switch {
	case disabled:
		line += styleDisabled.Render(" (disabled)")
	case focused:
		line = styleFocus.Render(line)
	case props.Variant == "primary":
		// Primary actions carry the accent color so they stand out from
		// the plain-bold secondary buttons and the fields alike.
		line = styleAccent.Render(line)
	default:
		// An unfocused button must still read as a button, not disabled
		// text: bold it (focus adds reverse video on top).
		line = styleFocused.Render(line)
	}
	return r.appendHints([]string{line}, hints)
}

// textFieldLines renders a TextField with its live editor. Both focus
// states keep the same enclosing brackets at the same fixed width; empty
// fields show their declared placeholder dim, or a blank interior when the
// agent declared none. longText fields are edited on one widened line
// (documented simplification).
func (r *viewRenderer) textFieldLines(f *focusable, focused bool) []string {
	props, ok := f.node.Component.Props.(*a2ui.TextFieldProps)
	if !ok {
		return nil
	}
	ctx := f.node.EvalContext(r.m.evalCtx())
	label := clean(props.Label.EvalString(ctx))
	bodyW := fieldBodyW(props.Variant)
	var body string
	if focused {
		body = fieldEditor(f.input.View(), bodyW)
	} else {
		shown := clean(f.input.Value())
		switch {
		case shown == "":
			if ph := clean(props.Placeholder.EvalString(ctx)); ph != "" {
				shown = styleDimW.Render(ph)
			}
		case props.Variant == "obscured":
			shown = strings.Repeat("•", len([]rune(shown)))
		}
		body = "[" + fieldInterior(shown, bodyW) + "]"
	}
	if props.Variant == "number" && focused {
		body += styleDimW.Render(" 0-9")
	}
	if focused && props.Variant == "longText" {
		body += styleDimW.Render("  (single-line editor)")
	}
	out := labeledInput(label, body)
	// Widget-local format validation for number variants.
	if props.Variant == "number" {
		if raw := strings.TrimSpace(f.input.Value()); raw != "" {
			if _, err := parseNumber(raw); err != nil {
				out = r.appendHint(out, styleHint.Render("✗ must be a number"))
			}
		}
	}
	return r.appendHints(out, r.checkHints(props.Checks, ctx, r.showChecks(f)))
}

// labeledInput aligns a labeled input in the form stack: the label is
// left-aligned and padded to labelCol so the input's brackets start at the
// same column as every other input's; a label longer than the column gets
// a line of its own above the input, which is indented by a small fixed
// amount (2 spaces) so its bracket sits just under the label instead of
// floating labelCol columns to the right.
func labeledInput(label, body string) []string {
	if label == "" {
		return []string{body}
	}
	if w := ansi.StringWidth(label); w <= labelCol-2 {
		return []string{label + ":" + strings.Repeat(" ", labelCol-1-w) + body}
	}
	return []string{label + ":", "  " + body}
}

// fieldEditor renders the focused editor's view inside accent brackets,
// normalized to the fixed interior width (the editor renders its Width in
// cells plus one cursor cell).
func fieldEditor(view string, bodyW int) string {
	return styleAccent.Render("[") + fieldInterior(view, bodyW) + styleAccent.Render("]")
}

// fieldInterior normalizes a field's bracket interior to the fixed cell
// width: truncate longer content, right-pad shorter content, so brackets
// never move between focus states or fields.
func fieldInterior(shown string, bodyW int) string {
	shown = ansi.Truncate(shown, bodyW, "")
	if pad := bodyW - ansi.StringWidth(shown); pad > 0 {
		return shown + strings.Repeat(" ", pad)
	}
	return shown
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
	return r.appendHints(out, r.checkHints(props.Checks, ctx, r.showChecks(f)))
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
	return r.appendHints(out, r.checkHints(props.Checks, ctx, r.showChecks(f)))
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
	bar := sliderBar(filled)
	if focused {
		bar = styleAccent.Render("[") + bar + styleAccent.Render("]")
	} else {
		bar = "[" + bar + "]"
	}
	line := label + ": " + a2ui.Stringify(value) + "/" + a2ui.Stringify(max) + " " + bar
	if focused {
		line += styleDimW.Render("  ←/→ step · ↑/↓ 10%")
	}
	out := []string{line}
	return r.appendHints(out, r.checkHints(props.Checks, ctx, r.showChecks(f)))
}

// sliderBar renders the slider track as three visually distinct zones: the
// filled cells in the accent color, the bold accent handle glyph sitting
// on the track at the fill boundary (index 0 when empty, so it stays
// visible at zero), and the dimmed unfilled cells — the affordance that
// the value is movable, not a read-only progress bar. The handle uses an
// explicit accent foreground without reverse video, so it reads as a thumb
// on the track instead of a detached block.
func sliderBar(filled int) string {
	blocks := maxIntW(0, filled-1)
	dim := sliderCells - blocks - 1
	return styleAccent.Render(strings.Repeat("█", blocks)) +
		styleHandle.Render("●") +
		styleDimW.Render(strings.Repeat("░", dim))
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
	var body string
	if focused {
		body = fieldEditor(f.input.View(), dateBodyW)
	} else {
		shown := clean(f.input.Value())
		if shown == "" {
			shown = styleDimW.Render(dateTimePlaceholder(props.EnableDate, props.EnableTime))
		}
		body = "[" + fieldInterior(shown, dateBodyW) + "]"
	}
	body += styleDimW.Render("  ISO 8601")
	out := labeledInput(label, body)
	if raw := strings.TrimSpace(f.input.Value()); raw != "" {
		if ts := parseDateTime(raw); ts == nil {
			out = r.appendHint(out, styleHint.Render("✗ invalid date/time (want "+dateTimePlaceholder(props.EnableDate, props.EnableTime)+")"))
		} else {
			if minRaw := strings.TrimSpace(props.Min.EvalString(ctx)); minRaw != "" {
				if min := parseDateTime(minRaw); min != nil && ts.Before(*min) {
					out = r.appendHint(out, styleHint.Render("✗ must be after "+minRaw))
				}
			}
			if maxRaw := strings.TrimSpace(props.Max.EvalString(ctx)); maxRaw != "" {
				if max := parseDateTime(maxRaw); max != nil && ts.After(*max) {
					out = r.appendHint(out, styleHint.Render("✗ must be before "+maxRaw))
				}
			}
		}
	}
	return r.appendHints(out, r.checkHints(props.Checks, ctx, r.showChecks(f)))
}

// checkHints renders the failing agent-defined checks of a component.
// Check-failure hints are suppressed on a pristine surface: they only show
// (per showChecks) once the widget's value was touched or a button
// activation was attempted.
func (r *viewRenderer) checkHints(checks []a2ui.CheckRule, ctx a2ui.EvalContext, show bool) []string {
	if !show {
		return nil
	}
	var out []string
	for _, rule := range checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			out = append(out, styleHint.Render("✗ "+checkMessage(res, rule)))
		}
	}
	return out
}

// showChecks reports whether agent check hints may render for a widget
// (nil for read-only previews): only after the user touched its value or
// attempted a button activation — never on the initial render.
func (r *viewRenderer) showChecks(f *focusable) bool {
	return r.m.activateAttempted || (f != nil && f.touched)
}

// appendHints appends each hint through appendHint.
func (r *viewRenderer) appendHints(out []string, hints []string) []string {
	for _, h := range hints {
		out = r.appendHint(out, h)
	}
	return out
}

// appendHint places a hint to the right of the widget's first line when it
// fits within the width (two-space gap), so rows below do not jump when a
// check flips while editing; otherwise it falls back to a line below.
// Hints arrive pre-styled; the fit check counts printable cells only.
func (r *viewRenderer) appendHint(out []string, hint string) []string {
	if len(out) == 0 {
		return append(out, hint)
	}
	fits := ansi.StringWidth(out[0])+hintGap+ansi.StringWidth(hint)+hintGutter <= r.width-hintMargin
	if !fits {
		return append(out, hint)
	}
	out[0] += strings.Repeat(" ", hintGap) + hint
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

func maxIntW(a, b int) int {
	if a > b {
		return a
	}
	return b
}
