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
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
	"github.com/HenryNebula/a2a-tui/internal/config"
	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

const inputRows = 3

// connectTimeout bounds a single card resolution attempt.
const connectTimeout = 10 * time.Second

// paneKind identifies the main-pane view currently displayed.
type paneKind int

const (
	paneTranscript paneKind = iota
	paneCard
	paneSurface
	paneTasks
	paneWire
	paneConsole
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

	// conn holds the current resolution (card etc.); session owns the
	// live connection once /connect succeeds.
	conn    *agent.Resolved
	connURL string // concrete URL of the current/last connection

	// Chat state.
	session       *agent.Session
	transcript    *chat.Transcript
	pending       *pendingInput // input-required task awaiting a reply
	lastTaskID    string
	lastContextID string // most recent agent context (taskless a2ui turns)
	inflight      int
	statusText    string
	streamMode    bool
	wireLog       *wirelog.Logger
	spinner       spinner.Model
	listening     bool
	pane          paneKind
	transcriptV   viewport.Model
	cardPane      CardPane
	surfacePane   SurfacePane
	tasksPane     TasksPane
	wirePane      WirePane
	consolePane   ConsolePane
	helpPane      HelpPane
	helpOpen      bool
	input         textarea.Model
	help          help.Model

	// pushPublicURL overrides the advertised webhook URL (--push-public-url
	// / A2A_TUI_PUSH_URL) for agents behind a tunnel.
	pushPublicURL string

	httpClient *http.Client
	connecting bool

	ready bool
}

// New returns the root application model. agentRef may be empty; store
// may be nil.
func New(agentRef string, mode agent.ProtocolMode, store *config.Store) *App {
	vp := viewport.New(0, 0)
	tr := chat.NewTranscript()
	tr.Append(chat.NewStatusBlock("a2a-tui — a terminal client for A2A agents."))
	tr.Append(chat.NewStatusBlock("Connect with /connect <url-or-name>, view the card with /card."))

	ta := textarea.New()
	ta.Placeholder = "Message the agent…  (/help for commands)"
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(inputRows)
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	app := &App{
		agentRef:    agentRef,
		protoMode:   mode,
		store:       store,
		connState:   "",
		transcript:  tr,
		wireLog:     wirelog.New(wirelog.DefaultRingSize),
		spinner:     sp,
		transcriptV: vp,
		cardPane:    NewCardPane(),
		tasksPane:   NewTasksPane(),
		wirePane:    NewWirePane(),
		consolePane: NewConsolePane(),
		helpPane:    NewHelpPane(),
		input:       ta,
		help:        help.New(),
		httpClient:  &http.Client{Timeout: 15 * time.Second},
	}
	// The console's default params reference the newest task; the closure
	// reads the (re)connected session dynamically.
	app.consolePane.lastTaskID = func() string {
		if app.session == nil {
			return app.lastTaskID
		}
		if list := app.session.Registry().List(); len(list) > 0 {
			return list[0].ID
		}
		return app.lastTaskID
	}
	return app
}

// SetPushPublicURL overrides the push webhook URL handed to agents
// (--push-public-url flag / A2A_TUI_PUSH_URL env). It only matters for
// remote agents that must reach a tunnel instead of 127.0.0.1.
func (a *App) SetPushPublicURL(url string) { a.pushPublicURL = url }

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
		a.refreshTranscript()

	case tea.KeyMsg:
		// The help overlay owns esc/?/f1 and the scroll keys while open; any
		// other key closes it and keeps routing (so ctrl+k still opens the
		// dashboard, ctrl+c still quits, typing still lands in the input).
		if a.helpOpen {
			if a.handleHelpKey(m) {
				return a, nil
			}
			a.helpOpen = false
		}
		switch {
		case keyMatches(m, keys.Quit):
			return a, tea.Quit
		case keyMatches(m, keys.PaneTranscript):
			a.pane = paneTranscript
			return a, nil
		case keyMatches(m, keys.PaneCard):
			a.pane = paneCard
			return a, nil
		case keyMatches(m, keys.PaneSurface):
			a.openSurface("")
			return a, nil
		case keyMatches(m, keys.PaneTasks):
			a.openTasksPane()
			return a, nil
		case keyMatches(m, keys.PaneWire):
			a.openWirePane()
			return a, nil
		case keyMatches(m, keys.PaneConsole):
			a.openConsolePane()
			return a, nil
		case a.pane == paneSurface:
			// The surface pane owns the keyboard (except the pane-switch
			// and quit globals above). Esc leaves the pane — unless an
			// openUrl prompt is active, where it cancels the prompt.
			if m.String() == "esc" && !a.surfacePane.PromptActive() {
				a.pane = paneTranscript
				return a, nil
			}
			cmd, _ := a.surfacePane.Update(m)
			return a, cmd
		case a.pane == paneTasks:
			// The dashboard owns the keyboard: esc leaves the detail view
			// (or the pane), enter/c/s/r act on the selected task.
			if m.String() == "esc" {
				if a.tasksPane.DetailOpen() {
					a.tasksPane.CloseDetail()
				} else {
					a.pane = paneTranscript
				}
				return a, nil
			}
			cmd, _ := a.tasksPane.Update(m, a)
			return a, cmd
		case a.pane == paneConsole:
			// The console owns the keyboard: esc leaves the pane, every
			// other key feeds the method/params editors.
			if m.String() == "esc" {
				a.pane = paneTranscript
				return a, nil
			}
			cmd, _ := a.consolePane.Update(m, a)
			return a, cmd
		case keyMatches(m, keys.Help) && (m.String() != "?" || !a.input.Focused()):
			// f1 always toggles help; "?" only when the chat input does not
			// have the keyboard — a focused input must receive the character.
			a.toggleHelp()
			return a, nil
		case keyMatches(m, keys.Cancel):
			return a, a.cancelActive()
		case m.String() == "enter":
			cmds = append(cmds, a.submit())
			return a, tea.Batch(cmds...)
		}
		// Route scroll keys to the card pane when it is displayed; a
		// consumed key never reaches the input or transcript.
		if a.pane == paneCard && a.cardPane.Update(msg) {
			return a, tea.Batch(cmds...)
		}
		// The wire pane consumes the scroll keys; everything else still
		// reaches the input box. Plain "c" would swallow a typed character
		// (the chat input keeps focus behind the pane), so clearing lives on
		// ctrl+l here and "c" is never forwarded to the pane.
		if a.pane == paneWire {
			if keyMatches(m, keys.WireClear) {
				a.wirePane.Clear()
				return a, tea.Batch(cmds...)
			}
			if m.String() != "c" && a.wirePane.Update(msg) {
				return a, tea.Batch(cmds...)
			}
		}

	case spinner.TickMsg:
		if a.inflight > 0 {
			var cmd tea.Cmd
			a.spinner, cmd = a.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
		if a.consolePane.inflight {
			var cmd tea.Cmd
			a.consolePane.spinner, cmd = a.consolePane.spinner.Update(msg)
			cmds = append(cmds, cmd)
		}
		return a, tea.Batch(cmds...)

	case consoleResultMsg:
		a.consolePane.applyResult(m)
		return a, nil

	case connectResultMsg:
		cmd := a.handleConnectResult(m)
		return a, cmd

	case agentEventMsg:
		a.listening = false
		return a, a.handleAgentEvent(m.ev)

	case agentEventsClosedMsg:
		a.listening = false
		return a, nil

	case taskResultMsg:
		a.handleTaskResult(m)
		return a, nil

	case tasksRefreshedMsg:
		// ListTasks discovery finished; the registry already absorbed the
		// results, so a plain re-render suffices.
		a.syncTasksPane()
		return a, nil
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	cmds = append(cmds, cmd)

	a.transcriptV, cmd = a.transcriptV.Update(msg)
	cmds = append(cmds, cmd)
	return a, tea.Batch(cmds...)
}

// cancelActive cancels in-flight sends/streams (Esc).
func (a *App) cancelActive() tea.Cmd {
	if a.session == nil || a.inflight == 0 {
		return nil
	}
	if a.session.CancelActive() > 0 {
		a.setStatus("cancelling…")
	}
	return nil
}

// Shutdown tears down the session (called by main after the program
// exits). Safe to call on a disconnected app.
func (a *App) Shutdown() {
	if a.session != nil {
		a.session.Shutdown()
		a.session = nil
	}
}

// handleConnectResult applies a finished resolution to header, transcript
// and card pane state, then arms the session listener.
func (a *App) handleConnectResult(m connectResultMsg) tea.Cmd {
	a.connecting = false
	if m.resolved != nil {
		a.cardPane.SetCard(m.resolved)
	}
	if m.err != nil {
		a.connState = "error"
		a.conn, a.connURL = nil, ""
		a.addError("connect", m.err)
		a.refreshTranscript()
		return nil
	}
	a.conn = m.resolved
	a.connURL = m.url
	a.agentName = m.resolved.Summary.Name
	a.protoVer = m.resolved.Wire
	a.connState = "connected"
	a.streamMode = m.resolved.Summary.Capabilities.Streaming

	stream := "✓"
	if !m.resolved.Summary.Capabilities.Streaming {
		stream = "✗"
	}
	push := "✓"
	if !m.resolved.Summary.Capabilities.PushNotifications {
		push = "✗"
	}
	a.addStatus("connected to " + a.agentName + ": A2A " + m.resolved.Wire +
		" · streaming " + stream + " · push " + push +
		" · endpoint " + m.resolved.BaseURL)

	if m.session != nil {
		if a.session != nil {
			// Fully retire the previous session (pumps, push webhook,
			// listener): StopPush alone left its goroutines running, and a
			// still-listening bridge would starve the new session's
			// armListener of the latch.
			old := a.session
			a.listening = false
			go old.Shutdown()
		}
		a.session = m.session
		a.lastTaskID = ""
		a.lastContextID = ""
		a.pending = nil
		// The new session owns a fresh A2UI engine and task registry;
		// drop the old panes' state.
		a.surfacePane.Close()
		a.tasksPane = NewTasksPane()
		if a.pane == paneSurface || a.pane == paneTasks {
			a.pane = paneTranscript
		}
	}
	// The raw console targets whatever connection was resolved (even when
	// no session object came back) and shares the wirelog transport.
	a.consolePane.SetTarget(m.resolved.BaseURL, a.consoleHTTPClient(), m.resolved.Wire)
	a.refreshTranscript()
	return a.armListener()
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
	return a.sendText(text)
}

// runCommand dispatches a slash command. Extended per milestone.
func (a *App) runCommand(line string) tea.Cmd {
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "/help":
		a.toggleHelp()

	case "/quit", "/exit":
		return tea.Quit

	case "/connect":
		if len(args) != 1 {
			a.addStatus("usage: /connect <agent-url-or-name>")
			a.refreshTranscript()
			return nil
		}
		if a.connecting {
			a.addStatus("already connecting — wait for the current attempt")
			a.refreshTranscript()
			return nil
		}
		return a.startConnect(args[0])

	case "/disconnect":
		if a.session != nil {
			s := a.session
			go s.Shutdown()
		}
		// Release the listener latch: the bridge listener selects on the
		// session's Done channel and exits on its own, but until it does,
		// a new session's armListener must not be blocked by it.
		a.listening = false
		a.session = nil
		a.conn = nil
		a.connURL = ""
		a.agentName, a.protoVer, a.connState = "", "", ""
		a.pending, a.lastTaskID, a.lastContextID = nil, "", ""
		a.inflight, a.statusText = 0, ""
		a.cardPane.SetCard(nil)
		a.surfacePane.Close()
		a.tasksPane = NewTasksPane()
		a.consolePane.Clear()
		if a.pane == paneSurface || a.pane == paneTasks || a.pane == paneConsole {
			a.pane = paneTranscript
		}
		a.addStatus("disconnected")
		a.refreshTranscript()
		return nil

	case "/agents":
		a.listAgents()
		a.refreshTranscript()

	case "/agent":
		cmd := a.agentCommand(args)
		a.refreshTranscript()
		return cmd

	case "/card":
		a.pane = paneCard
		if a.conn == nil {
			a.addStatus("no card yet — /connect <url-or-name> first")
			a.refreshTranscript()
		}

	case "/tasks":
		a.openTasksPane()

	case "/push":
		return a.cmdPush(args)

	case "/surface":
		a.cmdSurface(args)

	case "/chat":
		a.pane = paneTranscript

	case "/stream":
		return a.cmdStream(args)

	case "/wire":
		return a.cmdWire(args)

	case "/console":
		a.openConsolePane()

	case "/task":
		return a.cmdTask(args)

	case "/cancel":
		return a.cmdCancel(args)

	case "/history":
		return a.cmdHistory(args)

	case "/clear":
		return a.cmdClear()

	default:
		a.addStatus("unknown command: " + cmd)
		a.refreshTranscript()
	}
	return nil
}

// startConnect begins an async card resolution for ref (URL or saved
// agent name), then builds the connection and session.
func (a *App) startConnect(ref string) tea.Cmd {
	a.agentRef = ref
	a.connState = "connecting"
	a.connecting = true
	a.addStatus("resolving " + ref + "…")
	a.refreshTranscript()

	wireLog := a.wireLog
	httpClient := a.httpClient
	return func() tea.Msg {
		url, mode := a.expandRef(ref)
		ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
		defer cancel()
		res, err := agent.Resolve(ctx, httpClient, url, mode)
		if err != nil {
			return connectResultMsg{ref: ref, url: url, resolved: res, err: err}
		}
		conn, err := agent.NewConn(ctx, res, httpClient, agent.WithWireLog(wireLog))
		if err != nil {
			return connectResultMsg{ref: ref, url: url, resolved: res, err: err}
		}
		return connectResultMsg{ref: ref, url: url, resolved: res, session: agent.NewSession(conn)}
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
		a.addStatus("/agents: " + err.Error())
		return
	}
	if len(f.Agents) == 0 {
		a.addStatus("no saved agents — /connect, then /agent save <name>")
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
		a.addStatus(line)
	}
}

// agentCommand implements /agent save|remove|default.
func (a *App) agentCommand(args []string) tea.Cmd {
	if len(args) != 2 {
		a.addStatus("usage: /agent save|remove|default <name>")
		return nil
	}
	sub, name := args[0], args[1]

	f, err := a.store.Load()
	if err != nil {
		a.addStatus("/agent: " + err.Error())
		return nil
	}

	switch sub {
	case "save":
		if a.conn == nil {
			a.addStatus("no connection to save — /connect first")
			return nil
		}
		f.UpsertAgent(config.Agent{
			Name:     name,
			URL:      firstNonEmpty(a.connURL, a.conn.BaseURL),
			Protocol: a.conn.Wire,
		})
		if err := a.store.Save(f); err != nil {
			a.addStatus("/agent save: " + err.Error())
			return nil
		}
		a.addStatus("saved agent " + name + " → " + firstNonEmpty(a.connURL, a.conn.BaseURL))

	case "remove":
		if !f.RemoveAgent(name) {
			a.addStatus("no saved agent named " + name)
			return nil
		}
		if err := a.store.Save(f); err != nil {
			a.addStatus("/agent remove: " + err.Error())
			return nil
		}
		a.addStatus("removed agent " + name)

	case "default":
		if _, ok := f.Agent(name); !ok {
			a.addStatus("no saved agent named " + name)
			return nil
		}
		f.DefaultAgent = name
		if err := a.store.Save(f); err != nil {
			a.addStatus("/agent default: " + err.Error())
			return nil
		}
		a.addStatus("default agent: " + name)

	default:
		a.addStatus("unknown /agent subcommand: " + sub + " (save, remove, default)")
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

// layout recomputes sub-view geometry after a resize.
func (a *App) layout() {
	if a.width == 0 || a.height == 0 {
		return
	}
	inputHeight := inputRows + 2 // + borders
	a.input.SetWidth(a.width - 4)
	a.transcriptV.Width = a.width - 2
	a.transcriptV.Height = a.height - inputHeight - 3 // header + status + help
}

// View implements tea.Model.
func (a *App) View() string {
	if !a.ready {
		return "loading…"
	}
	header := a.headerView()
	body := a.transcriptV.View()
	switch a.pane {
	case paneCard:
		body = a.cardPane.View(a.width-2, a.transcriptV.Height)
	case paneSurface:
		body = a.surfacePane.View(a.width-2, a.transcriptV.Height)
	case paneTasks:
		body = a.tasksPane.View(a.width-2, a.transcriptV.Height)
	case paneWire:
		a.wirePane.Sync(a.wireLog.Snapshot())
		body = a.wirePane.View(a.width-2, a.transcriptV.Height)
	case paneConsole:
		body = a.consolePane.View(a.width-2, a.transcriptV.Height)
	}
	// The help overlay draws over whatever main pane is displayed.
	if a.helpOpen {
		body = a.helpPane.View(a.width-2, a.transcriptV.Height)
	}
	status := a.statusLineView()
	input := styleInputBox.Render(a.input.View())
	footer := a.helpView()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status, input, footer)
}

func (a *App) headerView() string {
	left := "a2a-tui"
	if a.agentName != "" {
		left += " · " + a.agentName
	}
	if a.protoVer != "" {
		left += " · A2A " + a.protoVer
	}
	if a.connState == "connected" {
		if a.streamMode {
			left += " · stream"
		}
		if a.session != nil && a.session.PushEnabled() {
			left += " · push"
		}
		if a.wireLog.Enabled() {
			left += " · wire"
		}
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

// statusLineView renders the one-line status bar: spinner while work is
// in flight, else the last status text.
func (a *App) statusLineView() string {
	if a.inflight > 0 {
		mode := "sending"
		if a.streamMode {
			mode = "streaming"
		}
		text := " " + mode + "…"
		if a.statusText != "" {
			text = " " + a.statusText
		}
		return styleStatus.Render(a.spinner.View() + text)
	}
	if a.statusText == "" {
		return ""
	}
	return styleStatus.Render(" " + a.statusText)
}

func (a *App) helpView() string {
	short := a.help.ShortHelpView([]key.Binding{
		keys.Send, keys.Cancel, keys.PaneTranscript, keys.PaneCard, keys.PaneSurface,
		keys.PaneTasks, keys.PaneWire, keys.PaneConsole, keys.Help, keys.Quit,
	})
	return styleHelp.Render(cell(short, max(20, a.width)))
}
