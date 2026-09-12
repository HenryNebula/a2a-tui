package tui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// commandHelp is the slash-command reference, one row per command — the
// single source of truth for both the /help and ? overlay and any future
// help rendering. Keep it in sync with runCommand.
var commandHelp = []struct{ name, args, desc string }{
	{"/connect", "<url-or-name>", "connect to an agent (URL or saved name)"},
	{"/disconnect", "", "drop the session"},
	{"/agents", "", "list saved agents"},
	{"/agent", "save|remove|default <name>", "manage saved agents"},
	{"/card", "", "show the agent card"},
	{"/tasks", "", "open the tasks dashboard"},
	{"/task", "<id>", "fetch a task snapshot"},
	{"/history", "<id> [n]", "task history (n most recent messages)"},
	{"/cancel", "<id>", "cancel a task"},
	{"/stream", "on|off", "toggle streaming sends"},
	{"/push", "on|off", "local push webhook; registers every new task"},
	{"/surface", "[id]", "focus an A2UI surface"},
	{"/wire", "on|off", "wire capture + raw frame pane"},
	{"/console", "", "raw JSON-RPC console"},
	{"/chat", "", "back to the transcript"},
	{"/clear", "", "clear the transcript"},
	{"/help", "", "this help"},
	{"/quit", "", "exit"},
}

// helpKeyRow is one key-hint row: the chord plus what it does.
type helpKeyRow struct {
	keys string
	desc string
}

// globalKeyRows renders the application-wide key.Binding help data (the
// same values the footer's short help is built from).
func globalKeyRows() []helpKeyRow {
	bindings := []key.Binding{
		keys.Send, keys.Cancel, keys.Help,
		keys.PaneTranscript, keys.PaneCard, keys.PaneSurface,
		keys.PaneTasks, keys.PaneWire, keys.PaneConsole, keys.Quit,
	}
	rows := make([]helpKeyRow, 0, len(bindings))
	for _, b := range bindings {
		if h := b.Help(); h.Key != "" {
			rows = append(rows, helpKeyRow{keys: h.Key, desc: h.Desc})
		}
	}
	return rows
}

// paneKeyRows are the per-pane key hints that only apply inside a pane.
var paneKeyRows = []helpKeyRow{
	{keys: "up/down", desc: "tasks: select · wire: scroll"},
	{keys: "enter", desc: "tasks: task detail"},
	{keys: "c", desc: "tasks: cancel"},
	{keys: "ctrl+l", desc: "wire: clear"},
	{keys: "s / r", desc: "tasks: subscribe / refresh"},
	{keys: "tab", desc: "surface: cycle fields · console: cycle methods"},
	{keys: "ctrl+enter", desc: "console: send (alt+enter works everywhere)"},
	{keys: "ctrl+v", desc: "console: A2A-Version toggle"},
	{keys: "up/down", desc: "console (method): history"},
	{keys: "[ ]", desc: "surface: switch live surfaces"},
	{keys: "y/n", desc: "surface: openUrl confirmation"},
}

// HelpPane is the full-help overlay: a two-column reference (keys left,
// commands right) in a scrollable viewport drawn over the main pane.
type HelpPane struct {
	viewport viewport.Model
	cache    string
	cacheW   int
}

// NewHelpPane returns an empty help overlay.
func NewHelpPane() HelpPane {
	return HelpPane{viewport: viewport.New(0, 0)}
}

// Update consumes the overlay's keys while it is open: ?/esc close it and
// the usual scroll bindings move the document. It reports whether the key
// was consumed; anything else falls through to the app (and closes the
// overlay — see App.Update).
func (p *HelpPane) Update(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false
	}
	switch k.String() {
	case "up", "k":
		p.viewport.ScrollUp(1)
	case "down", "j":
		p.viewport.ScrollDown(1)
	case "pgup":
		p.viewport.HalfPageUp()
	case "pgdown":
		p.viewport.HalfPageDown()
	case "home":
		p.viewport.GotoTop()
	case "end":
		p.viewport.GotoBottom()
	default:
		return false
	}
	return true
}

// View renders the two-column document into the given geometry.
func (p *HelpPane) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if p.cache == "" || p.cacheW != width {
		p.cacheW = width
		p.cache = p.render(width)
	}
	bodyHeight := max(1, height-1)
	p.viewport.Width = width
	p.viewport.Height = bodyHeight
	p.viewport.SetContent(p.cache)
	hint := "f1 / ? / esc close · up/down scroll"
	if n := strings.Count(p.cache, "\n") + 1; n > bodyHeight {
		// The document does not fit: make the hidden part discoverable.
		hint = "f1 / ? / esc close · up/down scroll · " +
			strconv.Itoa(int(p.viewport.ScrollPercent())) + "%"
	}
	footer := styleDim.Render(cell(hint, width))
	return p.viewport.View() + "\n" + footer
}

// render builds the full document: title, then keys and commands side by
// side (the commands table is the wider one; on narrow terminals the
// columns stack instead of wrapping).
func (p *HelpPane) render(width int) string {
	var b strings.Builder
	b.WriteString(styleCardTitle.Render(cell("a2a-tui · help", width)) + "\n\n")

	keysCol := keyColumn()
	cmdsCol := commandColumn()
	sep := strings.Repeat(" ", 3)
	if lipgloss.Width(keysCol)+lipgloss.Width(cmdsCol)+lipgloss.Width(sep) <= width {
		b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, keysCol, sep, cmdsCol))
	} else {
		b.WriteString(keysCol + "\n\n" + cmdsCol)
	}
	return b.String()
}

// keyColumn renders the keys table: global bindings, then per-pane hints.
func keyColumn() string {
	rows := append(globalKeyRows(), paneKeyRows...)
	return renderHelpTable("keys", rows, false)
}

// commandColumn renders the command table from commandHelp.
func commandColumn() string {
	rows := make([]helpKeyRow, 0, len(commandHelp))
	for _, c := range commandHelp {
		name := c.name
		if c.args != "" {
			name += " " + c.args
		}
		rows = append(rows, helpKeyRow{keys: name, desc: c.desc})
	}
	return renderHelpTable("commands", rows, true)
}

// renderHelpTable lays out one header + two-field table with the key
// column sized to its longest entry.
func renderHelpTable(header string, rows []helpKeyRow, stickyFirst bool) string {
	keyW := 0
	for _, r := range rows {
		if w := lipgloss.Width(r.keys); w > keyW {
			keyW = w
		}
	}
	// Long command signatures (e.g. /agent save|remove|default <name>)
	// wrap onto their own line instead of blowing up the column.
	maxKeyW := 30
	wrap := keyW > maxKeyW
	if wrap {
		keyW = maxKeyW
	}

	var b strings.Builder
	b.WriteString(styleCardHeader.Render(header) + "\n")
	for _, r := range rows {
		k, d := r.keys, r.desc
		if wrap && lipgloss.Width(k) > keyW {
			b.WriteString("  " + styleCardValue.Render(k) + "\n")
			b.WriteString("  " + styleDim.Render(strings.Repeat(" ", keyW)+"  "+d) + "\n")
			continue
		}
		if stickyFirst {
			b.WriteString("  " + styleCardLabel.Render(fitCell(k, keyW)) + "  " + styleCardValue.Render(d) + "\n")
		} else {
			b.WriteString("  " + styleCardValue.Render(fitCell(k, keyW)) + "  " + styleDim.Render(d) + "\n")
		}
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// App-side glue
// ---------------------------------------------------------------------------

// toggleHelp flips the help overlay (the ? key and /help both land here).
func (a *App) toggleHelp() {
	a.helpOpen = !a.helpOpen
	if a.helpOpen {
		a.helpPane.viewport.GotoTop()
	}
}

// handleHelpKey applies one key while the overlay is open. It reports
// whether the key was fully consumed; when it was not, the caller closes
// the overlay and continues with normal key routing.
func (a *App) handleHelpKey(m tea.KeyMsg) bool {
	switch m.String() {
	case "?", "esc", "f1":
		a.helpOpen = false
		return true
	}
	return a.helpPane.Update(m)
}
