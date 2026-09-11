package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// consoleSrv serves raw JSON-RPC posts for the console pane tests and
// records the last request it saw.
type consoleSrv struct {
	*httptest.Server
	last struct {
		method  string
		params  string
		version string
	}
}

// newConsoleSrv answers every POST with a GetTask-shaped result body.
func newConsoleSrv(t *testing.T) *consoleSrv {
	t.Helper()
	s := &consoleSrv{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var env struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &env)
		s.last.method = env.Method
		s.last.params = string(env.Params)
		s.last.version = r.Header.Get("A2A-Version")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"id":"t-1","kind":"task","status":{"state":"completed"}}}`)
	}))
	t.Cleanup(s.Close)
	return s
}

// newConsoleApp returns a test app whose console pane targets srv with the
// given dialect.
func newConsoleApp(t *testing.T, srv *consoleSrv, wire string) *App {
	t.Helper()
	a := newTestApp(t, nil)
	a.consolePane.SetTarget(srv.URL, a.consoleHTTPClient(), wire)
	return a
}

// runCmd executes a tea.Cmd, unwrapping one BatchMsg level, and returns the
// produced messages.
func runCmd(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	switch m := msg.(type) {
	case tea.BatchMsg:
		out := make([]tea.Msg, 0, len(m))
		for _, sub := range m {
			if sub == nil {
				continue
			}
			out = append(out, sub())
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

// sendKey presses a key through the app and returns the produced command.
func sendKey(t *testing.T, a *App, key tea.KeyMsg) tea.Cmd {
	t.Helper()
	return update(t, a, key)
}

func TestConsolePaneOpenAndPresets(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)

	// ctrl+e opens the pane.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyCtrlE})
	if a.pane != paneConsole {
		t.Fatal("ctrl+e did not open the console")
	}
	view := stripStyle(a.View())
	for _, want := range []string{"console", srv.URL, "SendMessage"} {
		if !strings.Contains(view, want) {
			t.Errorf("view missing %q:\n%s", want, view)
		}
	}

	// The first preset comes with default params installed.
	if got := a.consolePane.method.Value(); got != "SendMessage" {
		t.Fatalf("method = %q, want SendMessage", got)
	}
	if !strings.Contains(a.consolePane.params.Value(), `"role": "ROLE_USER"`) {
		t.Fatalf("default params missing user message skeleton:\n%s", a.consolePane.params.Value())
	}

	// Tab cycles through the v1.0 preset list in order.
	got := []string{"SendMessage"}
	for range len(presetsV1) - 1 {
		sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab})
		got = append(got, a.consolePane.method.Value())
	}
	if strings.Join(got, ",") != strings.Join(presetsV1, ",") {
		t.Fatalf("preset cycle = %v", got)
	}
	// ... and wraps.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab})
	if a.consolePane.method.Value() != presetsV1[0] {
		t.Fatal("preset cycle does not wrap")
	}

	// Switching to GetTask fills task-addressed defaults only while the
	// params field is empty.
	a.consolePane.params.SetValue("keep me")
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab}) // SendStreamingMessage
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab}) // GetTask
	if a.consolePane.method.Value() != "GetTask" {
		t.Fatalf("method = %q", a.consolePane.method.Value())
	}
	if a.consolePane.params.Value() != "keep me" {
		t.Fatalf("preset cycling overwrote user params:\n%s", a.consolePane.params.Value())
	}
}

func TestConsolePresetsV03(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV03)
	a.openConsolePane()

	if a.consolePane.method.Value() != presetsV03[0] {
		t.Fatalf("0.3 first preset = %q", a.consolePane.method.Value())
	}
	for range len(presetsV03) - 1 {
		sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab})
	}
	if a.consolePane.method.Value() != presetsV03[len(presetsV03)-1] {
		t.Fatalf("0.3 last preset = %q", a.consolePane.method.Value())
	}
	if !strings.Contains(a.consolePane.params.Value(), `"kind": "text"`) {
		t.Fatalf("0.3 default params missing text part:\n%s", a.consolePane.params.Value())
	}
	// Footer shows the auto version state: none for 0.3.
	if !strings.Contains(stripStyle(a.View()), "a2a-version: auto (none)") {
		t.Fatalf("version indicator missing:\n%s", stripStyle(a.View()))
	}
}

func TestConsoleGetTaskRoundTrip(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()

	// Cycle to GetTask (index 2) and let the defaults fill in, then send.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab})
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyTab})
	if a.consolePane.method.Value() != "GetTask" {
		t.Fatalf("method = %q", a.consolePane.method.Value())
	}
	a.consolePane.params.SetValue(`{"id":"t-1"}`)

	cmd := sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) // alt+enter sends
	if cmd == nil {
		t.Fatal("send produced no command")
	}
	if !a.consolePane.inflight {
		t.Fatal("pane not marked in flight")
	}
	var result *consoleResultMsg
	for _, msg := range runCmd(t, cmd) {
		if r, ok := msg.(consoleResultMsg); ok {
			result = &r
		}
	}
	if result == nil || result.err != nil {
		t.Fatalf("send produced %#v (err %v)", result, resultErr(result))
	}
	update(t, a, *result)

	// The server saw the GetTask request with params and the v1.0 header.
	if srv.last.method != "GetTask" {
		t.Errorf("server method = %q", srv.last.method)
	}
	if srv.last.params != `{"id":"t-1"}` {
		t.Errorf("server params = %s", srv.last.params)
	}
	if srv.last.version != "1.0" {
		t.Errorf("A2A-Version = %q, want 1.0 (auto on v1.0)", srv.last.version)
	}

	// The response is rendered pretty-printed.
	view := stripStyle(a.View())
	for _, want := range []string{`"id": "t-1"`, `"state": "completed"`} {
		if !strings.Contains(view, want) {
			t.Errorf("response missing %q:\n%s", want, view)
		}
	}
	if a.consolePane.inflight {
		t.Error("still in flight after the result")
	}

	// The exchange landed in the wire log too (shared transport).
	if last := a.wireLog.Last(); last == nil || !strings.Contains(string(last.ReqBody), "GetTask") {
		t.Fatalf("console call missing from the wire log: %+v", a.wireLog.Last())
	}
}

func resultErr(r *consoleResultMsg) error {
	if r == nil {
		return nil
	}
	return r.err
}

func TestConsoleHistoryCycling(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()

	// Two calls with different methods and params.
	for _, call := range []struct{ method, params string }{
		{"ListTasks", "{}"},
		{"GetTask", `{"id":"t-42"}`},
	} {
		a.consolePane.method.SetValue(call.method)
		a.consolePane.params.SetValue(call.params)
		cmd := sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
		for _, msg := range runCmd(t, cmd) {
			if r, ok := msg.(consoleResultMsg); ok {
				update(t, a, r)
			}
		}
	}

	// Up from a fresh form jumps to the newest exchange and fills both fields.
	a.consolePane.method.SetValue("")
	a.consolePane.params.SetValue("")
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyUp})
	if a.consolePane.method.Value() != "GetTask" {
		t.Fatalf("history fill method = %q", a.consolePane.method.Value())
	}
	if !strings.Contains(a.consolePane.params.Value(), `"t-42"`) {
		t.Fatalf("history fill params = %q", a.consolePane.params.Value())
	}
	if !strings.Contains(stripStyle(a.View()), "history 2/2") {
		t.Fatalf("history position missing:\n%s", stripStyle(a.View()))
	}

	// Up again reaches the older exchange.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyUp})
	if a.consolePane.method.Value() != "ListTasks" {
		t.Fatalf("older exchange = %q", a.consolePane.method.Value())
	}

	// Down returns to the newest, then one more clears the form.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyDown})
	if a.consolePane.method.Value() != "GetTask" {
		t.Fatalf("down = %q", a.consolePane.method.Value())
	}
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyDown})
	if a.consolePane.method.Value() != "" || a.consolePane.params.Value() != "" {
		t.Fatalf("form not cleared: %q / %q", a.consolePane.method.Value(), a.consolePane.params.Value())
	}
}

func TestConsoleInvalidParamsNeverSends(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()

	a.consolePane.params.SetValue(`{"id": broken`)
	cmd := sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	if cmd != nil {
		t.Fatal("invalid params must not fire a call")
	}
	if !strings.Contains(a.consolePane.status, "not valid JSON") {
		t.Fatalf("status = %q", a.consolePane.status)
	}
	if srv.last.method != "" {
		t.Fatalf("server saw %q despite invalid params", srv.last.method)
	}
}

func TestConsoleVersionToggle(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()

	// auto → on → off → auto.
	states := []string{"on (sends 1.0)", "off", "auto (1.0)"}
	for _, want := range states {
		sendKey(t, a, tea.KeyMsg{Type: tea.KeyCtrlV})
		if label := a.consolePane.versionLabel(); label != want {
			t.Fatalf("version label = %q, want %q", label, want)
		}
	}

	// Override off on a v1.0 connection: the header is not sent.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyCtrlV}) // on
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyCtrlV}) // off
	cmd := sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	for _, msg := range runCmd(t, cmd) {
		if r, ok := msg.(consoleResultMsg); ok {
			update(t, a, r)
		}
	}
	if srv.last.version != "" {
		t.Errorf("override off still sent A2A-Version %q", srv.last.version)
	}
}

func TestConsoleEnterMovesFocusShiftTabReturns(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()
	if !a.consolePane.methodFocus {
		t.Fatal("method should hold focus first")
	}
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.consolePane.methodFocus {
		t.Fatal("enter should move focus to params")
	}
	// Typing while params is focused edits the params field.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("{}")})
	if !strings.Contains(a.consolePane.params.Value(), "{}") {
		t.Fatalf("params = %q", a.consolePane.params.Value())
	}
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyShiftTab})
	if !a.consolePane.methodFocus {
		t.Fatal("shift+tab should return focus to method")
	}
}

func TestConsoleEscLeavesPane(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.pane != paneTranscript {
		t.Fatal("esc should leave the console pane")
	}
}

func TestConsoleCommandOpensPane(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.SetValue("/console")
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.pane != paneConsole {
		t.Fatal("/console did not open the pane")
	}
	// Without a target the pane explains itself.
	if !strings.Contains(stripStyle(a.View()), "console idle") {
		t.Fatalf("idle hint missing:\n%s", stripStyle(a.View()))
	}
}

func TestConsoleDefaultParamsUseLastTask(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()

	// Seed a task the way the session bridge would.
	a.session.Registry().Observe(a2a.NewStatusUpdateEvent(
		a2a.TaskInfo{TaskID: "t-9", ContextID: "c"}, a2a.TaskStateWorking, nil))

	a.consolePane.params.SetValue("") // let defaults fill
	a.consolePane.setDefaultsFor("GetTask")
	if got := a.consolePane.params.Value(); !strings.Contains(got, `"t-9"`) {
		t.Fatalf("default params did not use the registry task:\n%s", got)
	}
}

func TestConsoleSSEResponseRendering(t *testing.T) {
	sse := "data: {\"result\":{\"state\":\"working\"}}\n\n" +
		"data: {\"result\":{\"state\":\"completed\"}}\n\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	a := newTestApp(t, nil)
	a.consolePane.SetTarget(srv.URL, a.consoleHTTPClient(), agent.WireV1)
	a.openConsolePane()

	cmd := sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	for _, msg := range runCmd(t, cmd) {
		if r, ok := msg.(consoleResultMsg); ok {
			update(t, a, r)
		}
	}
	view := stripStyle(a.View())
	for _, want := range []string{"# frame 1", "# frame 2", `"state": "working"`, `"state": "completed"`} {
		if !strings.Contains(view, want) {
			t.Errorf("SSE rendering missing %q:\n%s", want, view)
		}
	}
}

func TestConsoleDisconnectClearsTarget(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()
	// The console owns the keyboard while open — leave it before typing
	// into the main input box.
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	a.input.SetValue("/disconnect")
	sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.consolePane.console != nil {
		t.Fatal("disconnect did not clear the console target")
	}
	if a.pane != paneTranscript {
		t.Fatal("disconnect should leave the console pane")
	}
}

// TestConsoleParamsValidatedOnSend guards the friendly error line for a
// params body that is valid text but invalid JSON.
func TestConsoleEmptyParamsStillSends(t *testing.T) {
	srv := newConsoleSrv(t)
	a := newConsoleApp(t, srv, agent.WireV1)
	a.openConsolePane()
	a.consolePane.method.SetValue("GetExtendedAgentCard")
	a.consolePane.params.SetValue("")

	cmd := sendKey(t, a, tea.KeyMsg{Type: tea.KeyEnter, Alt: true})
	for _, msg := range runCmd(t, cmd) {
		if r, ok := msg.(consoleResultMsg); ok {
			update(t, a, r)
		}
	}
	if srv.last.method != "GetExtendedAgentCard" {
		t.Fatalf("method = %q", srv.last.method)
	}
	if strings.Contains(srv.last.params, "params") {
		t.Fatalf("empty params should be omitted, got %q", srv.last.params)
	}
}
