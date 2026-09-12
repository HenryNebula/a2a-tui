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
	"strconv"
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

// Input-box placeholders by connection state.
const (
	placeholderConnected    = "Message the agent…  (/help for commands)"
	placeholderDisconnected = "Not connected — /connect <url-or-name> first  ·  /help for commands"
)

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

	// taskStart records when each task was first observed, so terminal
	// pills can show how long the task took.
	taskStart map[string]time.Time

	// lastSurfaceCount tracks live-surface growth: a newly created A2UI
	// surface is a prompt awaiting input, and prompts open pre-focused.
	lastSurfaceCount int

	// pendingNew counts transcript blocks that arrived while the user was
	// scrolled up (auto-follow is suspended for them); lastTranscriptLen
	// drives the delta.
	pendingNew        int
	lastTranscriptLen int
	cardPane          CardPane
	surfacePane       SurfacePane
	tasksPane         TasksPane
	wirePane          WirePane
	consolePane       ConsolePane
	helpPane          HelpPane
	helpOpen          bool
	input             textarea.Model
	help              help.Model

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
	tr.Append(chat.NewStatusBlockID("welcome-1", "a2a-tui — a terminal client for A2A agents."))
	tr.Append(chat.NewStatusBlockID("welcome-2", "Connect with /connect <url-or-name>, view the card with /card."))

	ta := textarea.New()
	ta.Placeholder = placeholderDisconnected
	ta.Prompt = "❯ "
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.SetHeight(1)
	// Bubbles' default prompt/placeholder styles paint a black background;
	// on any other terminal theme that shows up as a black box.
	for _, st := range []*lipgloss.Style{&ta.FocusedStyle.Placeholder, &ta.BlurredStyle.Placeholder} {
		*st = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	}
	for _, st := range []*lipgloss.Style{&ta.FocusedStyle.Prompt, &ta.BlurredStyle.Prompt} {
		*st = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	}
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	app := &App{
		agentRef:    agentRef,
		protoMode:   mode,
		store:       store,
		connState:   "",
		transcript:  tr,
		taskStart:   map[string]time.Time{},
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
		// A multi-rune key message is a fast-typed or pasted word — but
		// every bubbles editor matches bindings by msg.String(), so the
		// words "right"/"left"/"up"/"down"/"enter"/… arriving as one
		// message would be consumed as those KEYS: text vanishes, the
		// cursor jumps. Split the word into single runes so it behaves
		// exactly like slowly typed text (single-rune String()s never
		// collide with multi-character key names).
		if m.Type == tea.KeyRunes && len(m.Runes) > 1 {
			for _, r := range m.Runes {
				_, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}, Alt: m.Alt})
				cmds = append(cmds, cmd)
			}
			return a, tea.Batch(cmds...)
		}
		// The help overlay owns esc/?/f1 and the scroll keys while open; any
		// other key closes it and keeps routing (so ctrl+k still opens the
		// dashboard, ctrl+c still quits, typing still lands in the input).
		if a.helpOpen {
			if a.handleHelpKey(m) {
				return a, nil
			}
			a.helpOpen = false
		}
		// The card pane has no text entry of its own; esc or q returns
		// to the transcript instead of canceling a stream behind a
		// hidden input.
		if a.pane == paneCard && (m.String() == "esc" || m.String() == "q") {
			a.pane = paneTranscript
			return a, nil
		}
		// Same for the wire pane: esc goes back to the transcript (and
		// still cancels an in-flight stream, matching the footer's
		// esc-cancel promise).
		if a.pane == paneWire && m.String() == "esc" {
			a.pane = paneTranscript
			return a, a.cancelActive()
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
		case keyMatches(m, keys.Help):
			// f1 (or /help) toggles the overlay; no bare-character binding.
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
		// ctrl+l here and "c" is never forwarded to the pane. Arrows go to
		// the input while the user is typing (same arbitration as the
		// transcript).
		if a.pane == paneWire {
			if keyMatches(m, keys.WireClear) {
				a.wirePane.Clear()
				return a, tea.Batch(cmds...)
			}
			if m.String() != "c" && !scrollOwnedByInput(msg, a.input.Value()) && a.wirePane.Update(msg) {
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

	// With the input box hidden (keyboard-owning panes, help overlay) a
	// stray keypress must not silently edit the invisible textarea.
	if !a.inputVisible() && a.ready {
		return a, tea.Batch(cmds...)
	}
	// Arrow arbitration: with text in the input, Up/Down edit the
	// (possibly multi-line) input; with an empty input they scroll the
	// transcript — the user's state (typing vs browsing) picks the
	// owner, so keys never double-fire. PgUp/PgDn/Home/End always
	// scroll; everything else goes to the input (the viewport ignores
	// plain characters).
	var cmd tea.Cmd
	if !scrollOwnedByInput(msg, a.input.Value()) {
		// The bubbles viewport does not bind Home/End itself.
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "home":
				a.transcriptV.GotoTop()
			case "end":
				a.transcriptV.GotoBottom()
			}
		}
		a.transcriptV, cmd = a.transcriptV.Update(msg)
		cmds = append(cmds, cmd)
		// Returning to the bottom (End, PgDn, scrolling down) clears the
		// unread count — everything is on screen again.
		if a.pendingNew > 0 && a.transcriptV.AtBottom() {
			a.pendingNew = 0
		}
	}
	if !scrollOwnedByViewport(msg, a.input.Value()) {
		a.input, cmd = a.input.Update(msg)
		cmds = append(cmds, cmd)
	}
	a.fitInputHeight()
	return a, tea.Batch(cmds...)
}

// isScrollKey reports whether the message is one of the viewport scroll
// keys.
func isScrollKey(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false
	}
	switch k.String() {
	case "up", "down", "pgup", "pgdown", "home", "end":
		return true
	}
	return false
}

// scrollOwnedByInput reports whether a scroll-key message belongs to the
// input editor: the user is typing, so arrows edit text, not the view.
func scrollOwnedByInput(msg tea.Msg, input string) bool {
	return isScrollKey(msg) && strings.TrimSpace(input) != ""
}

// scrollOwnedByViewport reports whether a scroll-key message belongs to
// the viewport: the input is empty, so arrows browse history.
func scrollOwnedByViewport(msg tea.Msg, input string) bool {
	return isScrollKey(msg) && strings.TrimSpace(input) == ""
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

// setPlaceholder points the input placeholder at what the box can
// actually do right now: a reply target while a task waits on input, and
// a zoom-back hint while a live surface sits behind the transcript.
func (a *App) setPlaceholder() {
	switch {
	case a.pending != nil:
		a.input.Placeholder = "Reply to task #" + chat.ShortID(a.pending.taskID) + "…"
	case a.connState == "connected" && a.liveSurfaceCount() > 0:
		a.input.Placeholder = "Message the agent…  (^f back to the form · /help for commands)"
	case a.connState == "connected":
		a.input.Placeholder = placeholderConnected
	default:
		a.input.Placeholder = placeholderDisconnected
	}
}

// liveSurfaceCount reports how many A2UI surfaces are live (0 without a
// session).
func (a *App) liveSurfaceCount() int {
	if a.session == nil {
		return 0
	}
	return len(a.session.Engine().SurfaceIDs())
}

// fitInputHeight grows the input box with its content (paste can add
// newlines) instead of always reserving every row: one line by default,
// up to inputRows. The View pads below the footer so the total layout
// height stays stable.
func (a *App) fitInputHeight() {
	lines := strings.Count(a.input.Value(), "\n") + 1
	a.input.SetHeight(min(inputRows, max(1, lines)))
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
		a.setPlaceholder()
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
	a.setPlaceholder()

	stream := "✓"
	if !m.resolved.Summary.Capabilities.Streaming {
		stream = "✗"
	}
	push := "✓"
	if !m.resolved.Summary.Capabilities.PushNotifications {
		push = "✗"
	}
	// The onboarding hints only apply while disconnected; drop them so
	// the transcript opens with the connection line instead.
	a.transcript.RemoveByID("welcome-1")
	a.transcript.RemoveByID("welcome-2")
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
		// drop the old panes' state (and its auto-focus counter).
		a.surfacePane.Close()
		a.lastSurfaceCount = 0
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
	a.fitInputHeight()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// A plain message typed before the session exists (still connecting,
	// or the first connection failed) must not vanish: keep the draft in
	// the box so one more Enter sends it once connected.
	if a.session == nil && !strings.HasPrefix(text, "/") {
		a.input.SetValue(text)
		a.input.CursorEnd()
		if a.connecting {
			a.setStatus("still connecting — your message is kept, press enter again in a moment")
		} else {
			a.setStatus("not connected — /connect <url-or-name> first (your message is kept)")
		}
		a.setPlaceholder()
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
		a.setPlaceholder()
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

// inputVisible reports whether the chat input box (with its status line
// and footer) shares the screen with the main pane. Panes that own the
// keyboard — surface, tasks, console, card — and the help overlay take
// the whole area under the header instead; the wire pane keeps the input
// because typing there falls through to it.
func (a *App) inputVisible() bool {
	if a.helpOpen {
		return false
	}
	return a.pane == paneTranscript || a.pane == paneWire
}

// bodyHeight is the main-pane height for the current mode.
func (a *App) bodyHeight() int {
	if a.inputVisible() {
		return max(1, a.height-(inputRows+2)-3) // header + status + footer
	}
	return max(1, a.height-1) // header only
}

// layout recomputes sub-view geometry after a resize.
func (a *App) layout() {
	if a.width == 0 || a.height == 0 {
		return
	}
	a.input.SetWidth(a.width - 4)
	a.transcriptV.Width = a.width - 2
	a.transcriptV.Height = a.bodyHeight()
}

// View implements tea.Model.
func (a *App) View() string {
	if !a.ready {
		return "loading…"
	}
	header := a.headerView()
	if !a.inputVisible() {
		// Panes that own the keyboard fill everything under the header.
		bh := a.bodyHeight()
		body := a.paneBody(bh)
		// The help overlay draws over whatever main pane is displayed.
		if a.helpOpen {
			body = a.helpPane.View(a.width-2, bh)
		}
		return lipgloss.JoinVertical(lipgloss.Left, header, body)
	}
	// The transcript shares the screen with the input box: rows the
	// (dynamically sized) input does not use go to the body, and the
	// status row is always reserved so the layout never jumps.
	inputH := a.input.Height() + 2         // + borders
	bodyH := max(1, a.height-1-inputH-1-1) // header, status, footer
	a.transcriptV.Height = bodyH
	body := a.paneBody(bodyH)
	status := a.statusLineView()
	if status == "" {
		status = " "
	}
	input := styleInputBox.Render(a.input.View())
	footer := a.helpView()
	return lipgloss.JoinVertical(lipgloss.Left, header, body, status, input, footer)
}

// paneBody renders the main-pane body at the given height.
func (a *App) paneBody(height int) string {
	switch a.pane {
	case paneCard:
		return a.cardPane.View(a.width-2, height)
	case paneSurface:
		return a.surfacePane.View(a.width-2, height)
	case paneTasks:
		return a.tasksPane.View(a.width-2, height)
	case paneWire:
		a.wirePane.Sync(a.wireLog.Snapshot())
		return a.wirePane.View(a.width-2, height)
	case paneConsole:
		return a.consolePane.View(a.width-2, height)
	default:
		return a.transcriptV.View()
	}
}

func (a *App) headerView() string {
	// Pills from most to least important; the budget drops the tail so
	// the state badge never gets pushed off-screen on narrow terminals.
	state := a.connState
	if state == "" {
		state = "disconnected"
	}
	badge := state
	if a.width < 76 {
		badge = "●" // no room for the word
	}
	budget := max(12, a.width-lipgloss.Width(badge)-2)
	parts := []string{"a2a-tui"}
	if a.agentName != "" {
		parts = append(parts, truncateMiddle(a.agentName, budget-10))
	}
	if a.protoVer != "" && a.width >= 80 {
		parts = append(parts, "A2A "+a.protoVer)
	}
	if a.connState == "connected" && a.width >= 100 {
		if a.streamMode {
			parts = append(parts, "stream")
		}
		if a.session != nil && a.session.PushEnabled() {
			parts = append(parts, "push")
		}
		if a.wireLog.Enabled() {
			parts = append(parts, "wire")
		}
	}
	left := strings.Join(parts, " · ")
	for len(parts) > 1 && lipgloss.Width(left) > budget {
		parts = parts[:len(parts)-1]
		left = strings.Join(parts, " · ")
	}
	// One continuous top bar: a single subtle backdrop across the row
	// (no seam between pill and bar), title bold left, state right.
	bar := lipgloss.NewStyle().Background(lipgloss.Color("234"))
	renderedLeft := bar.Bold(true).Foreground(lipgloss.Color("231")).Render(" " + left + " ")
	right := styleState(state).Background(lipgloss.Color("234")).Render(" " + badge + " ")
	gapLen := max(1, a.width-lipgloss.Width(renderedLeft)-lipgloss.Width(right))
	line := renderedLeft + bar.Render(strings.Repeat(" ", gapLen)) + right
	// The header must be exactly one row on every terminal: clip rather
	// than let a long agent name wrap the bar.
	return cell(line, a.width)
}

// truncateMiddle shortens s to at most max runes, keeping both ends and
// marking the cut with an ellipsis ("" and short inputs pass through).
func truncateMiddle(s string, maxLen int) string {
	if maxLen <= 4 || lipgloss.Width(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	keep := maxLen - 1 // "…" costs one cell
	head := keep / 2
	tail := keep - head
	return string(runes[:head]) + "…" + string(runes[len(runes)-tail:])
}

// statusLineView renders the one-line status bar: a pending question
// outranks progress (the user's move beats the agent's), then unread
// arrivals while scrolled up, then the spinner while work is in flight,
// else the last status text.
func (a *App) statusLineView() string {
	if a.pending != nil {
		return styleSurfacePrompt.Render(cell(" input needed · #"+chat.ShortID(a.pending.taskID)+" — reply below", a.width))
	}
	if a.pendingNew > 0 {
		return styleSurfacePrompt.Render(cell(" ↓ "+strconv.Itoa(a.pendingNew)+" new · End jumps down", a.width))
	}
	if a.inflight > 0 {
		mode := "sending"
		if a.streamMode {
			mode = "streaming"
		}
		text := " " + mode + "…"
		if a.statusText != "" {
			text = " " + a.statusText
		}
		return styleStatus.Render(cell(a.spinner.View()+text, a.width))
	}
	if a.statusText == "" {
		return ""
	}
	// The status bar must occupy exactly one row: a long error string
	// left unclipped wraps on the real terminal, scrolling the frame and
	// smearing the previous render around the input box.
	return styleStatus.Render(cell(" "+a.statusText, a.width))
}

func (a *App) helpView() string {
	// The full bar must fit a 120-column terminal without truncation:
	// esc only earns a slot while something is in flight, and the pane
	// chords use the compact "^x" spelling. Very narrow terminals get the
	// minimal set.
	bindings := []key.Binding{keys.Send}
	if a.pane != paneTranscript {
		bindings = append(bindings, keys.PaneTranscript)
	}
	bindings = append(bindings, keys.PaneCard, keys.PaneSurface,
		keys.PaneTasks, keys.PaneWire, keys.PaneConsole, keys.Help)
	if a.inflight > 0 {
		bindings = append(bindings[:1], append([]key.Binding{keys.Cancel}, bindings[1:]...)...)
	}
	if a.width < 100 {
		bindings = []key.Binding{keys.Send, keys.Cancel, keys.Help, keys.Quit}
	}
	// The footer separator matches the header's middle dot.
	short := strings.ReplaceAll(a.help.ShortHelpView(bindings), " • ", " · ")
	return styleHelp.Render(cell(short, max(20, a.width)))
}
