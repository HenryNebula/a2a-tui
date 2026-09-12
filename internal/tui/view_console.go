package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
	"github.com/HenryNebula/a2a-tui/internal/rpcconsole"
)

// ConsolePane is the raw JSON-RPC console: a method field with per-dialect
// preset cycling, a params JSON editor, and a response view that shows
// exactly what the agent answered (pretty-printed, SSE frames separated).
// Calls run through rpcconsole on the session's wirelog-wrapped client, so
// every exchange is also visible in the /wire pane and cycleable from the
// pane's history.
//
// Keyboard (the pane owns the keyboard while open, like the tasks pane):
//
//	tab / shift+tab   cycle the method presets (fills default params when
//	                  the params field is empty)
//	up / down (method) cycle past exchanges (ctrl+p/ctrl+n too)
//	enter (method)     move to the params editor
//	shift+tab (params) back to the method field
//	ctrl+enter         send — alt+enter and ctrl+s work too (most terminals
//	                  cannot deliver ctrl+enter as a distinct key)
//	ctrl+v             toggle the A2A-Version header: auto → on → off
//	esc                leave the pane (handled by the App)
type ConsolePane struct {
	// console posts the requests; nil while disconnected.
	console *rpcconsole.Console
	// wire is the negotiated dialect ("1.0" or "0.3") driving presets and
	// the default A2A-Version behavior.
	wire string
	// endpoint is the display copy of the POST target.
	endpoint string

	method      textinput.Model
	params      textarea.Model
	viewport    viewport.Model
	spinner     spinner.Model
	methodFocus bool

	presetIdx       int
	versionOverride int // 0 auto, 1 on, 2 off
	inflight        bool
	// status is the transient info/error line above the footer.
	status string
	// response is the rendered answer document shown in the viewport.
	response string
	// historyIdx indexes console.History() while cycling (-1 = inactive).
	historyIdx int
	// lastTaskID supplies the most recent task ID for default params.
	lastTaskID func() string
}

// Method presets per dialect, in cycling order.
var (
	presetsV1  = []string{"SendMessage", "SendStreamingMessage", "GetTask", "ListTasks", "CancelTask", "SubscribeToTask", "CreateTaskPushNotificationConfig", "GetExtendedAgentCard"}
	presetsV03 = []string{"message/send", "message/stream", "tasks/get", "tasks/cancel", "tasks/resubscribe", "tasks/pushNotificationConfig/set"}
)

// paramsRows is the height of the params editor.
const paramsRows = 8

// maxConsoleBytes caps each pretty-printed response body / frame.
const maxConsoleBytes = 8 << 10 // 8KB

// NewConsolePane returns an unconnected console pane.
func NewConsolePane() ConsolePane {
	m := textinput.New()
	m.Prompt = ""
	m.Placeholder = "method (tab cycles presets)"
	m.CharLimit = 128

	p := textarea.New()
	p.Placeholder = `{"id": "…"}`
	p.CharLimit = 0
	p.SetHeight(paramsRows)
	p.ShowLineNumbers = false

	sp := spinner.New(spinner.WithSpinner(spinner.Dot))

	pane := ConsolePane{
		method:      m,
		params:      p,
		viewport:    viewport.New(0, 0),
		spinner:     sp,
		methodFocus: true,
		historyIdx:  -1,
	}
	pane.setFocus(true)
	return pane
}

// SetTarget binds the pane to a live POST target. client should be the
// session's client (wirelog-wrapped); wire selects the dialect's presets
// and the default A2A-Version behavior.
func (p *ConsolePane) SetTarget(baseURL string, client *http.Client, wire string) {
	p.console = rpcconsole.New(baseURL, client)
	p.wire = wire
	p.endpoint = baseURL
	p.reset()
	p.setDefaultsFor(presets(p.wire)[0])
}

// Clear drops the target (used by /disconnect).
func (p *ConsolePane) Clear() {
	p.console = nil
	p.wire = ""
	p.endpoint = ""
	p.reset()
}

// reset returns editor and cycling state to a blank slate.
func (p *ConsolePane) reset() {
	p.method.SetValue("")
	p.params.SetValue("")
	p.presetIdx = 0
	p.versionOverride = 0
	p.inflight = false
	p.status = ""
	p.response = ""
	p.historyIdx = -1
	p.setFocus(true)
	p.viewport.GotoTop()
	p.viewport.SetContent("")
}

// presets returns the dialect's method preset list.
func presets(wire string) []string {
	if wire == agent.WireV03 {
		return presetsV03
	}
	return presetsV1
}

// setDefaultsFor installs a preset method and, when the params field is
// empty, its default params body.
func (p *ConsolePane) setDefaultsFor(method string) {
	p.method.SetValue(method)
	p.presetIdx = indexOf(presets(p.wire), method)
	if strings.TrimSpace(p.params.Value()) == "" {
		p.params.SetValue(DefaultParams(method, p.wire, p.lastTask()))
	}
	p.historyIdx = -1
}

// lastTask resolves the most recent task ID for default params.
func (p *ConsolePane) lastTask() string {
	if p.lastTaskID != nil {
		return p.lastTaskID()
	}
	return ""
}

// indexOf returns the position of s in list (-1 when absent).
func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return 0
}

// DefaultParams builds a sensible params body for a preset method of the
// dialect. lastTaskID fills task-addressed bodies ("" leaves a placeholder).
func DefaultParams(method, wire string, lastTaskID string) string {
	id := lastTaskID
	if id == "" {
		id = "TASK-ID"
	}
	var v any
	switch method {
	case "SendMessage", "SendStreamingMessage":
		v = map[string]any{
			"message": map[string]any{
				"role":      "ROLE_USER",
				"parts":     []any{map[string]any{"text": "hello from the console"}},
				"messageId": "console-1",
			},
		}
	case "message/send", "message/stream":
		v = map[string]any{
			"message": map[string]any{
				"role":      "user",
				"parts":     []any{map[string]any{"kind": "text", "text": "hello from the console"}},
				"messageId": "console-1",
			},
		}
	case "GetTask", "CancelTask", "SubscribeToTask", "tasks/get", "tasks/cancel", "tasks/resubscribe":
		v = map[string]any{"id": id}
	case "ListTasks":
		return "{\n  \n}\n"
	case "CreateTaskPushNotificationConfig":
		v = map[string]any{
			"taskId": id,
			"pushNotificationConfig": map[string]any{
				"url": "http://127.0.0.1:9/push",
			},
		}
	case "tasks/pushNotificationConfig/set":
		v = map[string]any{
			"taskId": id,
			"pushNotificationConfig": map[string]any{
				"url": "http://127.0.0.1:9/push",
			},
		}
	case "GetExtendedAgentCard":
		return "" // no params member at all
	default:
		return "{\n  \n}\n"
	}
	return prettyJSON(rawJSON(v))
}

// Update routes one key through the pane. It returns commands and whether
// the key was consumed (the pane owns the keyboard while displayed).
func (p *ConsolePane) Update(msg tea.Msg, app *App) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}
	s := k.String()

	// Send works from either field. Terminals without the kitty keyboard
	// protocol cannot deliver ctrl+enter distinctly, so alt+enter and
	// ctrl+s are accepted as aliases.
	switch s {
	case "ctrl+enter", "alt+enter", "ctrl+s":
		return app.consoleSend(), true
	case "ctrl+v":
		p.versionOverride = (p.versionOverride + 1) % 3
		p.status = ""
		return nil, true
	}

	if p.methodFocus {
		switch s {
		case "tab":
			p.cyclePreset(1)
			return nil, true
		case "shift+tab":
			p.cyclePreset(-1)
			return nil, true
		case "up", "ctrl+p":
			p.cycleHistory(-1)
			return nil, true
		case "down", "ctrl+n":
			p.cycleHistory(1)
			return nil, true
		case "enter":
			p.setFocus(false)
			return nil, true
		case "pgup":
			p.viewport.HalfPageUp()
			return nil, true
		case "pgdown":
			p.viewport.HalfPageDown()
			return nil, true
		case "home":
			p.viewport.GotoTop()
			return nil, true
		case "end":
			p.viewport.GotoBottom()
			return nil, true
		}
		var cmd tea.Cmd
		p.method, cmd = p.method.Update(msg)
		return cmd, true
	}

	if s == "shift+tab" {
		p.setFocus(true)
		return nil, true
	}
	var cmd tea.Cmd
	p.params, cmd = p.params.Update(msg)
	return cmd, true
}

// setFocus moves keyboard focus between the method field and the params
// editor, keeping both bubbles' focus state in sync.
func (p *ConsolePane) setFocus(method bool) {
	p.methodFocus = method
	if method {
		p.params.Blur()
		p.method.Focus()
	} else {
		p.method.Blur()
		p.params.Focus()
	}
}

// cyclePreset moves through the dialect's preset list, wrapping.
func (p *ConsolePane) cyclePreset(delta int) {
	list := presets(p.wire)
	if len(list) == 0 {
		return
	}
	p.presetIdx = ((p.presetIdx+delta)%len(list) + len(list)) % len(list)
	p.method.SetValue(list[p.presetIdx])
	if strings.TrimSpace(p.params.Value()) == "" {
		p.params.SetValue(DefaultParams(list[p.presetIdx], p.wire, p.lastTask()))
	}
	p.historyIdx = -1
	p.status = ""
}

// cycleHistory steps through past exchanges (older with -1, newer with +1),
// filling the method and params fields from the recorded exchange.
func (p *ConsolePane) cycleHistory(delta int) {
	if p.console == nil {
		return
	}
	h := p.console.History()
	if len(h) == 0 {
		p.status = "history empty — send something first"
		return
	}
	switch {
	case p.historyIdx < 0:
		if delta < 0 {
			p.historyIdx = len(h) - 1 // start at the newest
		} else {
			return // nothing newer than a fresh form
		}
	default:
		p.historyIdx += delta
	}
	if p.historyIdx < 0 {
		p.historyIdx = 0
	}
	if p.historyIdx >= len(h) {
		p.historyIdx = -1 // stepped past the newest: blank form
		p.method.SetValue("")
		p.params.SetValue("")
		p.status = "back to a fresh form"
		return
	}
	ex := h[p.historyIdx]
	p.method.SetValue(ex.Method)
	if len(ex.Params) > 0 {
		if body := prettyJSON(ex.Params); body != "" {
			p.params.SetValue(body)
		}
	}
	p.presetIdx = indexOf(presets(p.wire), ex.Method)
	p.status = "history " + strconv.Itoa(p.historyIdx+1) + "/" + strconv.Itoa(len(h))
}

// applyResult renders a finished call into the response view.
func (p *ConsolePane) applyResult(m consoleResultMsg) {
	p.inflight = false
	p.response = renderConsoleResponse(m.respBody, m.frames)
	p.viewport.SetContent(p.response)
	p.viewport.GotoTop()
	switch {
	case m.err != nil:
		p.status = chat.SanitizeLine(agent.FriendlyError(m.err))
	default:
		p.status = ""
	}
}

// versionHeader computes the A2A-Version header value for the next call:
// auto sends "1.0" on v1.0 connections and nothing on 0.3; the ctrl+v
// override forces it on or off regardless of dialect.
func (p *ConsolePane) versionHeader() string {
	switch p.versionOverride {
	case 1:
		return "1.0"
	case 2:
		return ""
	}
	if p.wire == agent.WireV03 {
		return ""
	}
	return "1.0"
}

// versionLabel renders the footer's version-toggle state.
func (p *ConsolePane) versionLabel() string {
	value := "none"
	if p.wire != agent.WireV03 {
		value = "1.0"
	}
	switch p.versionOverride {
	case 1:
		return "on (sends 1.0)"
	case 2:
		return "off"
	}
	return "auto (" + value + ")"
}

// View renders the pane into the given geometry: head line, method field,
// params editor, scrollable response, status line and key-hint footer.
func (p *ConsolePane) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if p.console == nil {
		return styleDim.Render(cell("console idle — /connect <url-or-name> first, then ctrl+e or /console", width))
	}

	p.method.Width = max(20, width-10)
	p.params.SetWidth(max(20, width-2))

	status := p.statusLine(width)
	footer := p.footer(width)
	// Shrink the editor when the pane is too short for the full layout.
	editorRows := min(paramsRows+2, max(3, height-5)) // + textarea borders
	p.params.SetHeight(max(1, editorRows-2))

	head := "console · POST " + chat.SanitizeLine(p.endpoint) + " · A2A " + p.wire
	var b strings.Builder
	b.WriteString(styleCardLabel.Render(cell(head, width)) + "\n")

	if p.methodFocus {
		b.WriteString(styleCardLabel.Render("▸ method ") + p.method.View() + "\n")
	} else {
		b.WriteString(styleDim.Render("  method ") + p.method.View() + "\n")
	}

	if p.methodFocus {
		b.WriteString(styleDim.Render("  params (json)") + "\n")
	} else {
		b.WriteString(styleCardLabel.Render("▸ params (json)") + "\n")
	}
	for _, line := range strings.Split(p.params.View(), "\n") {
		b.WriteString(cell(line, width) + "\n")
	}

	responseHeight := height - 2 /*head*/ - 1 /*method*/ - 1 /*label*/ - editorRows - lipgloss.Height(status) - lipgloss.Height(footer)
	if responseHeight < 1 {
		responseHeight = 1
	}
	p.viewport.Width = width
	p.viewport.Height = responseHeight
	if p.response == "" {
		p.viewport.SetContent(styleDim.Render("response appears here — ctrl+enter sends"))
	} else {
		p.viewport.SetContent(p.response)
	}
	b.WriteString(p.viewport.View())
	if status != "" {
		b.WriteString("\n" + status)
	}
	b.WriteString("\n" + footer)
	return b.String()
}

// statusLine renders the spinner (call in flight) or the transient
// info/error row above the footer.
func (p *ConsolePane) statusLine(width int) string {
	if p.inflight {
		return styleStatus.Render(cell(p.spinner.View()+" calling "+chat.SanitizeLine(p.method.Value())+"…", width))
	}
	if p.status == "" {
		return ""
	}
	if strings.HasPrefix(p.status, "history ") {
		return styleDim.Render(cell(" "+p.status, width))
	}
	return styleError.Render(cell(" "+p.status, width))
}

// footer renders the key-hint row with the version-toggle state.
func (p *ConsolePane) footer(width int) string {
	hint := "ctrl+enter send · tab presets · enter→params · up/down history · ctrl+v a2a-version: " + p.versionLabel() + " · esc back"
	return styleDim.Render(cell(hint, width))
}

// ---------------------------------------------------------------------------
// Response rendering
// ---------------------------------------------------------------------------

// renderConsoleResponse pretty-prints a blocking body or the SSE frames,
// capped per body with an ellipsis marker.
func renderConsoleResponse(respBody []byte, frames []string) string {
	var b strings.Builder
	if len(respBody) > 0 {
		b.WriteString(prettyCapped(respBody))
	}
	for i, f := range frames {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styleDim.Render("# frame "+strconv.Itoa(i+1)) + "\n")
		b.WriteString(prettyCapped([]byte(f)))
	}
	return b.String()
}

// prettyCapped indents a JSON body (raw text when it does not parse),
// sanitizes it and caps it at maxConsoleBytes.
func prettyCapped(body []byte) string {
	truncated := false
	if len(body) > maxConsoleBytes {
		body = body[:maxConsoleBytes]
		truncated = true
	}
	out := prettyJSON(body)
	if out == "" {
		out = string(body)
	}
	out = chat.SanitizeText(out)
	if truncated {
		out += "\n… (truncated)"
	}
	return out
}

// prettyJSON indents raw JSON; "" when the body does not parse.
func prettyJSON(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return ""
	}
	return buf.String()
}

// rawJSON marshals v or returns a minimal object on failure.
func rawJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// ---------------------------------------------------------------------------
// App-side glue
// ---------------------------------------------------------------------------

// consoleResultMsg carries the outcome of one console call.
type consoleResultMsg struct {
	respBody []byte
	frames   []string
	err      error
}

// openConsolePane switches to the console (ctrl+e, /console).
func (a *App) openConsolePane() {
	a.pane = paneConsole
}

// consoleSend validates the form and fires the call as a tea.Cmd. It never
// blocks Update: the POST runs inside the returned command.
func (a *App) consoleSend() tea.Cmd {
	p := &a.consolePane
	if p.console == nil {
		p.status = "not connected — /connect first"
		return nil
	}
	if p.inflight {
		p.status = "a call is already in flight"
		return nil
	}
	method := strings.TrimSpace(p.method.Value())
	if method == "" {
		p.status = "method is empty — type one or press tab for presets"
		return nil
	}
	var params json.RawMessage
	if text := strings.TrimSpace(p.params.Value()); text != "" {
		if !json.Valid([]byte(text)) {
			p.status = "params are not valid JSON"
			return nil
		}
		params = json.RawMessage(text)
	}

	console, version := p.console, p.versionHeader()
	p.inflight = true
	p.status = ""
	call := func() tea.Msg {
		respBody, frames, err := console.Call(context.Background(), method, params, version, nil)
		return consoleResultMsg{respBody: respBody, frames: frames, err: err}
	}
	return tea.Batch(call, p.spinner.Tick)
}

// consoleHTTPClient builds the console's HTTP client: a copy of the app
// client with the wirelog transport wrapped in (so every console call also
// lands in /wire) and no client-level timeout (Call bounds itself).
func (a *App) consoleHTTPClient() *http.Client {
	hc := &http.Client{}
	if a.httpClient != nil {
		*hc = *a.httpClient
	}
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	hc.Transport = a.wireLog.Transport(transport)
	hc.Timeout = 0
	return hc
}
