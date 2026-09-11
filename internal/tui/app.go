// Package tui implements the a2a-tui terminal application.
//
// App is the single root Bubble Tea model. Sub-views (chat transcript, task
// dashboard, agent card, raw console, A2UI surface) are plain structs owned
// by App with Update(msg tea.Msg) and View(width, height int) string
// methods — keeping one source of truth and trivial focus routing.
package tui

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/config"
)

const inputRows = 3

// connectTimeout bounds a single card resolution attempt.
const connectTimeout = 10 * time.Second

// paneKind identifies the main-pane view currently displayed.
type paneKind int

const (
	paneTranscript paneKind = iota
	paneCard
)

// App is the root application model.
type App struct {
	width, height int

	// agentRef is the requested agent (flag or /connect): a URL or a
	// saved agent name.
	agentRef string

	// protoMode is the requested wire protocol (flag, saved agent, or
	// /connect override).
	protoMode agent.ProtocolMode

	// store persists saved agents and credentials; nil degrades
	// saved-agent features gracefully.
	store *config.Store

	// Header state.
	agentName string
	protoVer  string
	connState string // "", "connecting", "connected", "error"

	// conn holds the current connection. M3 replaces it with a full
	// agent.Session.
	conn    *agent.Resolved
	connURL string // concrete URL of the current/last connection

	pane       paneKind
	transcript viewport.Model
	cardPane   CardPane
	input      textarea.Model
	help       help.Model

	httpClient *http.Client
	connecting bool

	ready bool
}

// New returns the root application model. agentRef may be empty; store
// may be nil.
func New(agentRef string, mode agent.ProtocolMode, store *config.Store) *App {
	vp := viewport.New(0, 0)
	vp.SetContent(welcomeText())

	ta := textarea.New()
	ta.Placeholder = "Message the agent…  (/help for commands)"
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(inputRows)
	ta.Focus()

	return &App{
		agentRef:   agentRef,
		protoMode:  mode,
		store:      store,
		connState:  "",
		transcript: vp,
		cardPane:   NewCardPane(),
		input:      ta,
		help:       help.New(),
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

func welcomeText() string {
	return styleDim.Render("a2a-tui — a terminal client for A2A agents.") + "\n" +
		styleDim.Render("Connect with /connect <url-or-name>, view the card with /card.")
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd {
	if a.agentRef != "" {
		return a.startConnect(a.agentRef)
	}
	return textarea.Blink
}

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		a.layout()
		a.ready = true

	case tea.KeyMsg:
		switch {
		case keyMatches(m, keys.Quit):
			return a, tea.Quit
		case keyMatches(m, keys.Help):
			a.appendTranscript(styleDim.Render("keys: ctrl+c quit · enter send · alt+enter/ctrl+j newline · ctrl+t transcript · ctrl+g card · ? help"))
		case keyMatches(m, keys.PaneTranscript):
			a.pane = paneTranscript
			return a, nil
		case keyMatches(m, keys.PaneCard):
			a.pane = paneCard
			return a, nil
		case m.String() == "enter":
			cmds = append(cmds, a.submit())
			return a, tea.Batch(cmds...)
		}
		// Route scroll keys to the card pane when it is displayed; a
		// consumed key never reaches the input or transcript.
		if a.pane == paneCard && a.cardPane.Update(msg) {
			return a, tea.Batch(cmds...)
		}

	case connectResultMsg:
		a.handleConnectResult(m)
		return a, nil
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	cmds = append(cmds, cmd)

	a.transcript, cmd = a.transcript.Update(msg)
	cmds = append(cmds, cmd)
	return a, tea.Batch(cmds...)
}

// handleConnectResult applies a finished resolution to header, transcript
// and card pane state.
func (a *App) handleConnectResult(m connectResultMsg) {
	a.connecting = false
	if m.err != nil {
		a.connState = "error"
		a.appendTranscript(styleError.Render("connect failed: " + m.err.Error()))
		return
	}
	a.conn = m.resolved
	a.connURL = m.url
	a.agentName = m.resolved.Summary.Name
	a.protoVer = m.resolved.Wire
	a.connState = "connected"
	a.cardPane.SetCard(m.resolved)

	stream := "✓"
	if !m.resolved.Summary.Capabilities.Streaming {
		stream = "✗"
	}
	push := "✓"
	if !m.resolved.Summary.Capabilities.PushNotifications {
		push = "✗"
	}
	a.appendTranscript(styleDim.Render(
		"connected to " + a.agentName + ": A2A " + m.resolved.Wire +
			" · streaming " + stream + " · push " + push +
			" · endpoint " + m.resolved.BaseURL))
}

// submit handles Enter in the input box: slash commands, else chat send.
func (a *App) submit() tea.Cmd {
	text := strings.TrimRight(a.input.Value(), "\n")
	a.input.Reset()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return a.runCommand(text)
	}
	a.appendTranscript(styleUser.Render("┃ you: ") + text)
	a.appendTranscript(styleDim.Render("  (not connected — chat lands with the next milestone)"))
	return nil
}

// runCommand dispatches a slash command. Extended per milestone.
func (a *App) runCommand(line string) tea.Cmd {
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "/help":
		a.appendTranscript(styleDim.Render(
			"commands: /help /connect <url-or-name> /disconnect /agents /agent save|remove|default <name> /card /chat /quit"))

	case "/quit", "/exit":
		return tea.Quit

	case "/connect":
		if len(args) != 1 {
			a.appendTranscript(styleError.Render("usage: /connect <agent-url-or-name>"))
			return nil
		}
		if a.connecting {
			a.appendTranscript(styleError.Render("already connecting — wait for the current attempt"))
			return nil
		}
		return a.startConnect(args[0])

	case "/disconnect":
		a.conn = nil
		a.connURL = ""
		a.agentName, a.protoVer, a.connState = "", "", ""
		a.cardPane.SetCard(nil)
		a.appendTranscript(styleDim.Render("disconnected"))

	case "/agents":
		a.listAgents()

	case "/agent":
		return a.agentCommand(args)

	case "/card":
		a.pane = paneCard
		if a.conn == nil {
			a.appendTranscript(styleDim.Render("no card yet — /connect <url-or-name> first"))
		}

	case "/chat":
		a.pane = paneTranscript

	default:
		a.appendTranscript(styleError.Render("unknown command: " + cmd))
	}
	return nil
}

// startConnect begins an async card resolution for ref (URL or saved
// agent name) and marks the header state.
func (a *App) startConnect(ref string) tea.Cmd {
	a.agentRef = ref
	a.connState = "connecting"
	a.connecting = true
	a.appendTranscript(styleDim.Render("resolving " + ref + "…"))

	return func() tea.Msg {
		url, mode := a.expandRef(ref)
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		res, err := agent.Resolve(ctx, a.httpClient, url, mode)
		return connectResultMsg{ref: ref, url: url, resolved: res, err: err}
	}
}

// expandRef turns a saved agent name into its URL (and protocol pin).
// URL-ish input and unknown names pass through untouched so bare hosts
// keep working.
func (a *App) expandRef(ref string) (string, agent.ProtocolMode) {
	mode := a.protoMode
	if isURLish(ref) {
		return ref, mode
	}
	f, err := a.store.Load()
	if err != nil {
		return ref, mode // fall back to bare-host interpretation
	}
	if ag, ok := f.Agent(ref); ok {
		if mode == agent.Auto && ag.Protocol != "" {
			if m, err := agent.ParseProtocolMode(ag.Protocol); err == nil {
				mode = m
			}
		}
		return ag.URL, mode
	}
	return ref, mode
}

// isURLish reports whether ref should be treated as a URL rather than a
// saved agent name.
func isURLish(ref string) bool {
	return strings.Contains(ref, "://") ||
		strings.ContainsAny(ref, "/.") ||
		strings.HasPrefix(ref, "localhost")
}

// listAgents prints the saved agent list to the transcript.
func (a *App) listAgents() {
	f, err := a.store.Load()
	if err != nil {
		a.appendTranscript(styleError.Render("/agents: " + err.Error()))
		return
	}
	if len(f.Agents) == 0 {
		a.appendTranscript(styleDim.Render("no saved agents — /connect, then /agent save <name>"))
		return
	}
	for _, ag := range f.Agents {
		marker := "  "
		if ag.Name == f.DefaultAgent {
			marker = "★ "
		}
		line := marker + ag.Name + " → " + ag.URL
		if ag.Protocol != "" {
			line += "  (A2A " + ag.Protocol + ")"
		}
		a.appendTranscript(styleDim.Render(line))
	}
}

// agentCommand implements /agent save|remove|default.
func (a *App) agentCommand(args []string) tea.Cmd {
	if len(args) != 2 {
		a.appendTranscript(styleError.Render("usage: /agent save|remove|default <name>"))
		return nil
	}
	sub, name := args[0], args[1]

	f, err := a.store.Load()
	if err != nil {
		a.appendTranscript(styleError.Render("/agent: " + err.Error()))
		return nil
	}

	switch sub {
	case "save":
		if a.conn == nil {
			a.appendTranscript(styleError.Render("no connection to save — /connect first"))
			return nil
		}
		f.UpsertAgent(config.Agent{
			Name:     name,
			URL:      firstNonEmpty(a.connURL, a.conn.BaseURL),
			Protocol: a.conn.Wire,
		})
		if err := a.store.Save(f); err != nil {
			a.appendTranscript(styleError.Render("/agent save: " + err.Error()))
			return nil
		}
		a.appendTranscript(styleDim.Render("saved agent " + name + " → " + firstNonEmpty(a.connURL, a.conn.BaseURL)))

	case "remove":
		if !f.RemoveAgent(name) {
			a.appendTranscript(styleError.Render("no saved agent named " + name))
			return nil
		}
		if err := a.store.Save(f); err != nil {
			a.appendTranscript(styleError.Render("/agent remove: " + err.Error()))
			return nil
		}
		a.appendTranscript(styleDim.Render("removed agent " + name))

	case "default":
		if _, ok := f.Agent(name); !ok {
			a.appendTranscript(styleError.Render("no saved agent named " + name))
			return nil
		}
		f.DefaultAgent = name
		if err := a.store.Save(f); err != nil {
			a.appendTranscript(styleError.Render("/agent default: " + err.Error()))
			return nil
		}
		a.appendTranscript(styleDim.Render("default agent: " + name))

	default:
		a.appendTranscript(styleError.Render("unknown /agent subcommand: " + sub + " (save, remove, default)"))
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// appendTranscript adds a line to the transcript and keeps it glued to the
// bottom when the user has not scrolled up.
func (a *App) appendTranscript(line string) {
	atBottom := a.transcript.AtBottom()
	cur := a.transcript.View()
	a.transcript.SetContent(cur + strings.TrimRight(line, "\n") + "\n")
	if atBottom {
		a.transcript.GotoBottom()
	}
}

// layout recomputes sub-view geometry after a resize.
func (a *App) layout() {
	if a.width == 0 || a.height == 0 {
		return
	}
	inputHeight := inputRows + 2 // + borders
	a.input.SetWidth(a.width - 4)
	a.transcript.Width = a.width - 2
	a.transcript.Height = a.height - inputHeight - 2 // header + help
}

// View implements tea.Model.
func (a *App) View() string {
	if !a.ready {
		return "loading…"
	}
	header := a.headerView()
	body := a.transcript.View()
	if a.pane == paneCard {
		body = a.cardPane.View(a.width-2, a.transcript.Height)
	}
	input := styleInputBox.Render(a.input.View())
	footer := a.helpView()
	gap := lipgloss.NewStyle().Height(1).Render("")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, gap, input, footer)
}

func (a *App) headerView() string {
	left := "a2a-tui"
	if a.agentName != "" {
		left += " · " + a.agentName
	}
	if a.protoVer != "" {
		left += " · A2A " + a.protoVer
	}
	state := a.connState
	if state == "" {
		state = "disconnected"
	}
	renderedLeft := styleHeader.Render(left)
	right := styleState(state).Render(" " + state + " ")
	gap := strings.Repeat(" ", max(1, a.width-lipgloss.Width(renderedLeft)-lipgloss.Width(right)))
	return renderedLeft + gap + right
}

func (a *App) helpView() string {
	return styleHelp.Render(a.help.ShortHelpView([]key.Binding{
		keys.Send, keys.PaneTranscript, keys.PaneCard, keys.Help, keys.Quit,
	}))
}
