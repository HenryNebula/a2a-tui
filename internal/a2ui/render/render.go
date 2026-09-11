// Package render renders A2UI surfaces to plain terminal text. It is a
// static renderer: interactive components are displayed as value previews,
// the first tab of a Tabs component is shown, and modals render their
// trigger with the content indented below.
package render

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	a2ui "github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// Options tune a render pass.
type Options struct {
	// MaxHeight caps the rendered line count (default DefaultMaxHeight).
	MaxHeight int
	// Funcs overrides the function registry used for dynamic evaluation.
	Funcs *a2ui.FunctionRegistry
}

// DefaultMaxHeight is the default output height budget (4x24 lines).
const DefaultMaxHeight = 96

// minColumnWidth is the floor width for Row children sharing space.
const minColumnWidth = 6

// maxRenderNodes bounds how many nodes a single render may expand.
const maxRenderNodes = 4000

// Render renders the surface's materialized tree to a plain-text block no
// wider than width columns and no taller than DefaultMaxHeight lines.
func Render(surf *a2ui.Surface, width int) string {
	return RenderWithOptions(surf, width, Options{})
}

// RenderWithOptions renders with explicit options.
func RenderWithOptions(surf *a2ui.Surface, width int, opts Options) string {
	if surf == nil || width < 1 {
		return ""
	}
	if opts.MaxHeight <= 0 {
		opts.MaxHeight = DefaultMaxHeight
	}
	funcs := opts.Funcs
	if funcs == nil {
		funcs = a2ui.NewFunctionRegistry()
	}
	r := &renderer{
		surf:    surf,
		width:   width,
		maxH:    opts.MaxHeight,
		funcs:   funcs,
		rootCtx: surf.EvalContextFor(funcs),
	}
	root, err := surf.Materialize()
	if err != nil {
		if strings.Contains(err.Error(), "no root component") {
			return ""
		}
		return clip(r.formatUnsupported(fmt.Sprintf("surface error: %v", err), "", width), r.maxH, width)
	}
	out := r.node(root)
	return clip(out, r.maxH, width)
}

type renderer struct {
	surf    *a2ui.Surface
	width   int
	maxH    int
	funcs   *a2ui.FunctionRegistry
	rootCtx a2ui.EvalContext
	nodes   int
}

// node renders one tree node (with its collection scope for evaluation).
func (r *renderer) node(n *a2ui.Node) string {
	if n == nil {
		return ""
	}
	r.nodes++
	if r.nodes > maxRenderNodes {
		return "…"
	}
	if n.Placeholder || n.Component == nil {
		return r.formatUnsupported(fmt.Sprintf("unresolved component %q", n.RefID), "", r.width)
	}
	ctx := n.EvalContext(r.rootCtx)
	switch props := n.Component.Props.(type) {
	case *a2ui.TextProps:
		return r.text(props, ctx)
	case *a2ui.ImageProps:
		return r.media("image", props.URL, props.Description, ctx)
	case *a2ui.VideoProps:
		return r.media("video", props.URL, a2ui.Dynamic{}, ctx)
	case *a2ui.AudioPlayerProps:
		return r.media("audio", props.URL, props.Description, ctx)
	case *a2ui.IconProps:
		return r.icon(props, ctx)
	case *a2ui.RowProps:
		return r.row(n, props)
	case *a2ui.ColumnProps:
		return r.column(n, props)
	case *a2ui.ListProps:
		return r.list(n, props, ctx)
	case *a2ui.CardProps:
		return r.card(n, ctx)
	case *a2ui.TabsProps:
		return r.tabs(n, props, ctx)
	case *a2ui.ModalProps:
		return r.modal(n, ctx)
	case *a2ui.DividerProps:
		return r.divider(props)
	case *a2ui.ButtonProps:
		return r.button(n, props, ctx)
	case *a2ui.TextFieldProps:
		return r.textField(props, ctx)
	case *a2ui.CheckBoxProps:
		return r.checkBox(props, ctx)
	case *a2ui.ChoicePickerProps:
		return r.choicePicker(props, ctx)
	case *a2ui.SliderProps:
		return r.slider(props, ctx)
	case *a2ui.DateTimeInputProps:
		return r.dateTime(props, ctx)
	case *a2ui.UnknownProps:
		return r.formatUnsupported(
			fmt.Sprintf("unsupported component %q", n.Component.Component),
			string(props.Raw), r.width)
	default:
		return r.formatUnsupported(
			fmt.Sprintf("unsupported component %q", n.Component.Component),
			string(n.Component.Raw), r.width)
	}
}

// ---------------------------------------------------------------------------
// Leaf components
// ---------------------------------------------------------------------------

func (r *renderer) text(p *a2ui.TextProps, ctx a2ui.EvalContext) string {
	content := StripMarkdown(clean(p.Text.EvalString(ctx)))
	style := lipgloss.NewStyle()
	switch p.Variant {
	case "h1", "h2", "h3":
		style = style.Bold(true)
	case "h4", "h5":
		style = style.Underline(true)
	}
	return wrapPlain(style.Render(content), r.width)
}

func (r *renderer) media(kind string, url, desc a2ui.Dynamic, ctx a2ui.EvalContext) string {
	u := clean(url.EvalString(ctx))
	line := "[" + kind
	if u != "" {
		line += " " + u
	}
	if d := clean(desc.EvalString(ctx)); d != "" {
		line += " — " + d
	}
	return wrapPlain(line+"]", r.width)
}

func (r *renderer) icon(p *a2ui.IconProps, ctx a2ui.EvalContext) string {
	switch p.Name.Kind {
	case a2ui.IconKindEnum:
		return Glyph(p.Name.Enum)
	case a2ui.IconKindSVG:
		return "◆ " + clean(p.Name.SVGPath.EvalString(ctx))
	default:
		name := clean(p.Name.Dynamic.EvalString(ctx))
		if name == "" {
			return Glyph("")
		}
		return Glyph(name)
	}
}

func (r *renderer) divider(p *a2ui.DividerProps) string {
	if p.Axis == "vertical" {
		return "│"
	}
	return strings.Repeat("─", maxInt(1, r.width))
}

// ---------------------------------------------------------------------------
// Layout components
// ---------------------------------------------------------------------------

func (r *renderer) row(n *a2ui.Node, _ *a2ui.RowProps) string {
	kids := n.Children
	if len(kids) == 0 {
		return ""
	}
	gap := 1
	avail := r.width - gap*(len(kids)-1)
	if avail < 1 {
		avail = 1
	}
	weights := make([]float64, len(kids))
	total := 0.0
	for i, k := range kids {
		w := 1.0
		if k.Component != nil && k.Component.Weight != nil && *k.Component.Weight > 0 {
			w = *k.Component.Weight
		}
		weights[i] = w
		total += w
	}
	widths := make([]int, len(kids))
	used := 0
	for i := range kids {
		w := int(float64(avail) * weights[i] / total)
		if w < minColumnWidth {
			w = minColumnWidth
		}
		widths[i] = w
		used += w
	}
	// Trim overshoot from the widest column.
	if used > avail {
		for i := range widths {
			if widths[i] > minColumnWidth {
				widths[i] -= used - avail
				if widths[i] < minColumnWidth {
					widths[i] = minColumnWidth
				}
				break
			}
		}
	}
	blocks := make([]string, len(kids))
	for i, k := range kids {
		inner := r.renderAt(k, widths[i])
		// Pad each column to its allocated width so JoinHorizontal keeps
		// columns visually separated even for multi-line content.
		blocks[i] = lipgloss.NewStyle().Width(widths[i]).MaxWidth(widths[i]).Render(inner)
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, blocks...)
}

func (r *renderer) column(n *a2ui.Node, p *a2ui.ColumnProps) string {
	var lines []string
	for _, k := range n.Children {
		lines = append(lines, r.node(k))
	}
	sep := "\n"
	if p.Justify == "spaceBetween" || p.Justify == "spaceAround" || p.Justify == "spaceEvenly" {
		sep = "\n\n"
	}
	return strings.Join(lines, sep)
}

func (r *renderer) list(n *a2ui.Node, p *a2ui.ListProps, _ a2ui.EvalContext) string {
	if len(n.Children) == 0 {
		return ""
	}
	if p.Direction == "horizontal" {
		blocks := make([]string, len(n.Children))
		for i, k := range n.Children {
			blocks[i] = r.node(k)
		}
		return strings.Join(blocks, " ")
	}
	var lines []string
	for _, k := range n.Children {
		lines = append(lines, r.node(k))
	}
	return strings.Join(lines, "\n")
}

func (r *renderer) card(n *a2ui.Node, _ a2ui.EvalContext) string {
	inner := ""
	if len(n.Children) == 1 {
		inner = r.renderAt(n.Children[0], maxInt(0, r.width-4))
	}
	if strings.TrimSpace(inner) == "" {
		inner = " "
	}
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1).
		MaxWidth(r.width)
	return clip(box.Render(inner), r.maxH, r.width)
}

func (r *renderer) tabs(n *a2ui.Node, p *a2ui.TabsProps, ctx a2ui.EvalContext) string {
	var titles []string
	for i, t := range p.Tabs {
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
	bar := clip(strings.Join(titles, " | "), 1, r.width)
	var content string
	if len(n.Children) > 0 {
		content = r.node(n.Children[0]) // static renderer: show first tab
	}
	if content == "" {
		return bar
	}
	return bar + "\n" + content
}

func (r *renderer) modal(n *a2ui.Node, _ a2ui.EvalContext) string {
	var parts []string
	if len(n.Children) > 0 {
		parts = append(parts, r.node(n.Children[0]))
	}
	if len(n.Children) > 1 {
		parts = append(parts, clip("┄┄ modal ┄┄", 1, r.width))
		content := r.node(n.Children[1])
		for _, line := range strings.Split(content, "\n") {
			parts = append(parts, "  "+line)
		}
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// Interactive components (static value previews)
// ---------------------------------------------------------------------------

func (r *renderer) button(n *a2ui.Node, p *a2ui.ButtonProps, ctx a2ui.EvalContext) string {
	label := ""
	if len(n.Children) > 0 {
		label = strings.Join(strings.Split(r.node(n.Children[0]), "\n"), " ")
	}
	label = strings.TrimSpace(label)
	disabled := false
	for _, rule := range p.Checks {
		if !a2ui.EvalCheck(rule, ctx).Valid() {
			disabled = true
			break
		}
	}
	var line string
	switch p.Variant {
	case "primary":
		line = "[[ " + label + " ]]"
	case "borderless":
		line = label
	default:
		line = "[ " + label + " ]"
	}
	if disabled {
		line = line + " (disabled)"
	}
	if p.Action != nil && p.Action.Event != nil {
		line += " → " + clean(p.Action.Event.Name)
	}
	return wrapPlain(line, r.width)
}

func (r *renderer) textField(p *a2ui.TextFieldProps, ctx a2ui.EvalContext) string {
	label := clean(p.Label.EvalString(ctx))
	value := clean(p.Value.EvalString(ctx))
	shown := value
	if p.Variant == "obscured" && value != "" {
		shown = strings.Repeat("•", len([]rune(value)))
	}
	if shown == "" {
		if ph := clean(p.Placeholder.EvalString(ctx)); ph != "" {
			shown = "(" + ph + ")"
		} else {
			shown = "(empty)"
		}
	}
	line := label + ": [" + shown + "]"
	for _, rule := range p.Checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			line += "  ✗ " + checkMessage(res, rule)
		}
	}
	return wrapPlain(line, r.width)
}

func checkMessage(res a2ui.ValidationResult, rule a2ui.CheckRule) string {
	return clean(a2ui.CheckDisplayMessage(res, rule))
}

func (r *renderer) checkBox(p *a2ui.CheckBoxProps, ctx a2ui.EvalContext) string {
	mark := "[ ]"
	if p.Value.EvalBoolean(ctx) {
		mark = "[x]"
	}
	line := mark + " " + clean(p.Label.EvalString(ctx))
	for _, rule := range p.Checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			line += "  ✗ " + checkMessage(res, rule)
		}
	}
	return wrapPlain(line, r.width)
}

func (r *renderer) choicePicker(p *a2ui.ChoicePickerProps, ctx a2ui.EvalContext) string {
	selected := map[string]bool{}
	for _, v := range p.Value.EvalStringList(ctx) {
		selected[v] = true
	}
	markerOn, markerOff := "(•)", "( )"
	if p.Variant == "multipleSelection" {
		markerOn, markerOff = "[x]", "[ ]"
	}
	var lines []string
	if label := clean(p.Label.EvalString(ctx)); label != "" {
		lines = append(lines, label+":")
	}
	for _, opt := range p.Options {
		marker := markerOff
		if selected[opt.Value] {
			marker = markerOn
		}
		lines = append(lines, "  "+marker+" "+clean(opt.Label.EvalString(ctx)))
	}
	if len(lines) == 0 {
		return ""
	}
	return clip(strings.Join(lines, "\n"), r.maxH, r.width)
}

func (r *renderer) slider(p *a2ui.SliderProps, ctx a2ui.EvalContext) string {
	label := clean(p.Label.EvalString(ctx))
	value := p.Value.EvalNumber(ctx)
	min, max := 0.0, 100.0
	if p.Min != nil {
		min = *p.Min
	}
	if p.Max != nil {
		max = *p.Max
	}
	if max <= min {
		max = min + 1
	}
	barCells := 8
	filled := int((value - min) / (max - min) * float64(barCells))
	if filled < 0 {
		filled = 0
	}
	if filled > barCells {
		filled = barCells
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barCells-filled)
	line := fmt.Sprintf("%s: %s/%s [%s]", label,
		a2ui.Stringify(value), a2ui.Stringify(max), bar)
	return wrapPlain(line, r.width)
}

func (r *renderer) dateTime(p *a2ui.DateTimeInputProps, ctx a2ui.EvalContext) string {
	value := clean(p.Value.EvalString(ctx))
	if value == "" {
		value = "(unset)"
	}
	line := value
	if label := clean(p.Label.EvalString(ctx)); label != "" {
		line = label + ": " + line
	}
	return wrapPlain(line, r.width)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// renderAt renders a child constrained to w columns.
func (r *renderer) renderAt(n *a2ui.Node, w int) string {
	if w < 1 {
		w = 1
	}
	saved := r.width
	r.width = w
	out := r.node(n)
	r.width = saved
	return clip(out, r.maxH, w)
}

// formatUnsupported renders the placeholder box for unknown or unresolved
// components, with a bounded JSON peek.
func (r *renderer) formatUnsupported(title, rawJSON string, width int) string {
	body := clean(title)
	if rawJSON != "" {
		peek := clean(rawJSON)
		if len(peek) > 120 {
			peek = peek[:120] + "…"
		}
		body += "\n" + strings.ReplaceAll(peek, "\n", " ")
	}
	if width < 12 {
		return clip(body, r.maxH, width)
	}
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		Padding(0, 1).
		MaxWidth(width)
	return clip(box.Render(body), r.maxH, width)
}

// wrapPlain wraps text at width columns on spaces, falling back to hard
// truncation for unbreakable content.
func wrapPlain(s string, width int) string {
	if s == "" {
		return ""
	}
	if width < 1 {
		width = 1
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, wrapLine(para, width)...)
	}
	return strings.Join(out, "\n")
}

func wrapLine(line string, width int) []string {
	if lipgloss.Width(line) <= width {
		return []string{line}
	}
	words := strings.Fields(line)
	if len(words) == 0 {
		return []string{truncateCells(line, width)}
	}
	var lines []string
	cur := ""
	for _, word := range words {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if lipgloss.Width(cand) <= width {
			cur = cand
			continue
		}
		if cur != "" {
			lines = append(lines, cur)
		}
		if lipgloss.Width(word) <= width {
			cur = word
		} else {
			// Break a single over-long word.
			for lipgloss.Width(word) > width {
				var head string
				head, word = splitCells(word, width)
				lines = append(lines, head)
			}
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// truncateCells cuts s to at most width display cells, ending with "…".
func truncateCells(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	if width <= 1 {
		return "…"
	}
	var b strings.Builder
	acc := 0
	for _, rn := range s {
		w := lipgloss.Width(string(rn))
		if acc+w > width-1 {
			break
		}
		b.WriteRune(rn)
		acc += w
	}
	return b.String() + "…"
}

// splitCells splits s at width display cells (first part without marker).
// It guarantees forward progress: the returned head is non-empty for any
// non-empty input, so a rune wider than the whole budget (e.g. a double-cell
// CJK rune at width 1) is still consumed — callers looping until the rest
// fits would spin forever otherwise.
func splitCells(s string, width int) (string, string) {
	runes := []rune(s)
	var b strings.Builder
	acc := 0
	for i, rn := range runes {
		w := lipgloss.Width(string(rn))
		if acc > 0 && acc+w > width {
			return b.String(), string(runes[i:])
		}
		b.WriteRune(rn)
		acc += w
	}
	return b.String(), ""
}

// clip enforces the height and width budgets on the final block, adding the
// focus hint when truncation occurred.
func clip(block string, maxH, width int) string {
	if block == "" {
		return ""
	}
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = truncateCells(strings.TrimRight(line, ""), width)
	}
	if len(lines) > maxH {
		if maxH >= 2 {
			lines = append(lines[:maxH-2], "…", "(full view: focus surface)")
		} else {
			lines = lines[:maxH]
		}
	}
	return strings.Join(lines, "\n")
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
