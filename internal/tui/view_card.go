package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// CardPane renders a resolved agent card (agent.CardSummary) in its own
// scrollable viewport. Content is pre-sanitized and size-capped by the
// resolver; rendering truncates again to the pane width so no line can
// wrap unpredictably or overflow.
type CardPane struct {
	viewport viewport.Model
	card     *agent.Resolved
	w, h     int // last rendered geometry
}

// NewCardPane returns an empty card pane.
func NewCardPane() CardPane {
	return CardPane{viewport: viewport.New(0, 0)}
}

// SetCard installs a resolved card and resets scrolling.
func (p *CardPane) SetCard(r *agent.Resolved) {
	p.card = r
	p.w = 0 // force re-render on next View
	p.viewport.GotoTop()
}

// Update consumes navigation keys (up/down, pgup/pgdn, home/end). It
// deliberately handles only keys that never type into the input box, and
// reports whether msg was consumed.
func (p *CardPane) Update(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false
	}
	switch k.String() {
	case "up":
		p.viewport.LineUp(1)
	case "down":
		p.viewport.LineDown(1)
	case "pgup":
		p.viewport.HalfViewUp()
	case "pgdown":
		p.viewport.HalfViewDown()
	case "home":
		p.viewport.GotoTop()
	case "end":
		p.viewport.GotoBottom()
	default:
		return false
	}
	return true
}

// View renders the card into the given geometry.
func (p *CardPane) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if width != p.w || height != p.h {
		p.w, p.h = width, height
		p.viewport.Width = width
		p.viewport.Height = height
		p.viewport.SetContent(p.render(width))
	}
	return p.viewport.View()
}

// render lays out the full card document. All card-derived strings pass
// through cell (ansi-aware truncation) before drawing.
func (p *CardPane) render(width int) string {
	if p.card == nil {
		return styleDim.Render("no agent card — /connect <url> first, then /card")
	}
	s := p.card.Summary

	var b strings.Builder
	b.WriteString(styleCardTitle.Render(cell(s.Name, width)) + "\n")
	meta := []string{}
	if s.Provider != "" {
		meta = append(meta, cell(s.Provider, width))
	}
	if s.Version != "" {
		meta = append(meta, "v"+cell(s.Version, width))
	}
	if len(meta) > 0 {
		b.WriteString(styleDim.Render(strings.Join(meta, " · ")) + "\n")
	}

	proto := "A2A " + p.card.Wire
	if s.ProtocolVersion != "" && s.ProtocolVersion != p.card.Wire {
		proto += " (card " + cell(s.ProtocolVersion, width) + ")"
	}
	b.WriteString(styleCardLabel.Render("protocol ") + styleCardValue.Render(proto) + "\n")
	b.WriteString(styleCardLabel.Render("endpoint ") + styleCardValue.Render(cell(p.card.BaseURL, width)) + "\n")

	stream := styleCardOff.Render("streaming ✗")
	if s.Capabilities.Streaming {
		stream = styleCardOK.Render("streaming ✓")
	}
	push := styleCardOff.Render("push ✗")
	if s.Capabilities.PushNotifications {
		push = styleCardOK.Render("push ✓")
	}
	b.WriteString(stream + "  " + push + "\n")

	if s.Description != "" {
		b.WriteString("\n" + styleCardValue.Render(ansi.Wrap(cell(s.Description, maxDescrip), max(20, width-2), "")) + "\n")
	}

	b.WriteString("\n" + styleCardLabel.Render("interfaces") + "\n")
	rows := make([][]string, 0, len(s.Interfaces))
	for _, ifc := range s.Interfaces {
		rows = append(rows, []string{ifc.URL, ifc.Binding, ifc.ProtocolVersion, ifc.Tenant})
	}
	b.WriteString(table([]string{"url", "binding", "version", "tenant"}, rows, width))

	b.WriteString("\n" + styleCardLabel.Render("security schemes") + "\n")
	if len(s.SecuritySchemes) == 0 {
		b.WriteString(styleDim.Render("  (none declared — open agent)") + "\n")
	} else {
		for _, sc := range s.SecuritySchemes {
			details := make([]string, 0, len(sc.Details))
			for k, v := range sc.Details {
				details = append(details, k+"="+v)
			}
			sort.Strings(details)
			line := "  " + sc.ID + " (" + sc.Type + ")"
			if len(details) > 0 {
				line += " " + strings.Join(details, " ")
			}
			b.WriteString(styleCardValue.Render(cell(line, width)) + "\n")
		}
	}

	b.WriteString("\n" + styleCardLabel.Render("skills") + "\n")
	if len(s.Skills) == 0 {
		b.WriteString(styleDim.Render("  (none)") + "\n")
	} else {
		srows := make([][]string, 0, len(s.Skills))
		for _, sk := range s.Skills {
			srows = append(srows, []string{sk.ID, sk.Name, strings.Join(sk.Tags, ","), sk.Description})
		}
		b.WriteString(table([]string{"id", "name", "tags", "description"}, srows, width))
	}

	modes := ""
	if len(s.DefaultInputModes) > 0 {
		modes += "in: " + strings.Join(s.DefaultInputModes, ", ")
	}
	if len(s.DefaultOutputModes) > 0 {
		if modes != "" {
			modes += "  "
		}
		modes += "out: " + strings.Join(s.DefaultOutputModes, ", ")
	}
	if modes != "" {
		b.WriteString("\n" + styleCardLabel.Render("modes ") + styleDim.Render(cell(modes, width)) + "\n")
	}

	return b.String()
}

// maxDescrip keeps the description cell well under the summary cap so
// the rendered document stays a sane size even on huge cards.
const maxDescrip = 512

// table renders header and rows as fixed-width columns fitted to width.
// The last column absorbs remaining space; every cell is truncated
// ansi-aware so no agent string can exceed its column.
func table(header []string, rows [][]string, width int) string {
	if len(rows) == 0 {
		return styleDim.Render("  (none)") + "\n"
	}
	const gap = "  "
	cols := len(header)

	// Natural column widths from content.
	w := make([]int, cols)
	for i, h := range header {
		w[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if cw := lipgloss.Width(row[i]); cw > w[i] {
				w[i] = cw
			}
		}
	}

	// Fit to width: shrink the widest non-id column until the row fits.
	budget := width - 2 // leading indent
	gapLen := lipgloss.Width(gap)
	total := func() int {
		t := 0
		for _, c := range w {
			t += c
		}
		return t + gapLen*(cols-1)
	}
	for total() > budget && cols > 1 {
		widest := 1
		for i := 1; i < cols; i++ { // never shrink the id column
			if w[i] > w[widest] {
				widest = i
			}
		}
		if w[widest] <= 4 {
			break
		}
		w[widest]--
	}

	var b strings.Builder
	head := make([]string, cols)
	for i, h := range header {
		head[i] = styleCardHeader.Render(fitCell(h, w[i]))
	}
	b.WriteString("  " + strings.Join(head, gap) + "\n")
	for _, row := range rows {
		cells := make([]string, cols)
		for i := 0; i < cols; i++ {
			v := ""
			if i < len(row) {
				v = row[i]
			}
			cells[i] = styleCardValue.Render(fitCell(v, w[i]))
		}
		b.WriteString("  " + strings.Join(cells, gap) + "\n")
	}
	return b.String()
}

// fitCell truncates s to exactly n display cells (ansi-aware).
func fitCell(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s + strings.Repeat(" ", n-lipgloss.Width(s))
	}
	return ansi.Truncate(s, n, "…")
}

// cell truncates s to at most n display cells without padding.
func cell(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}
