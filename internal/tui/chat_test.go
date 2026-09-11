package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// fakeConn records sends for assertions; all other methods are inert.
type fakeConn struct {
	mu     sync.Mutex
	sends  []*a2a.SendMessageRequest
	gets   []a2a.GetTaskRequest
	seq    func(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
	stream bool // whether SendStreaming uses the scripted seq
}

func (f *fakeConn) Card() *a2a.AgentCard           { return nil }
func (f *fakeConn) CardSummary() agent.CardSummary { return agent.CardSummary{} }
func (f *fakeConn) WireVersion() string            { return agent.WireV1 }
func (f *fakeConn) BaseURL() string                { return "http://fake" }
func (f *fakeConn) ListTasks(context.Context) (*a2a.ListTasksResponse, error) {
	return &a2a.ListTasksResponse{}, nil
}
func (f *fakeConn) CancelTask(_ context.Context, id string) (*a2a.Task, error) {
	return nil, a2a.ErrTaskNotCancelable
}
func (f *fakeConn) GetTask(_ context.Context, id string, h *int) (*a2a.Task, error) {
	f.mu.Lock()
	f.gets = append(f.gets, a2a.GetTaskRequest{ID: a2a.TaskID(id), HistoryLength: h})
	f.mu.Unlock()
	return sampleTask(), nil
}
func (f *fakeConn) CreateTaskPushConfig(_ context.Context, cfg *a2a.PushConfig) (*a2a.PushConfig, error) {
	return cfg, nil
}
func (f *fakeConn) ListTaskPushConfigs(context.Context, string) ([]*a2a.PushConfig, error) {
	return nil, nil
}
func (f *fakeConn) DeleteTaskPushConfig(context.Context, string, string) error { return nil }
func (f *fakeConn) GetExtendedAgentCard(context.Context) (*a2a.AgentCard, error) {
	return nil, errors.New("not configured")
}
func (f *fakeConn) Destroy() error { return nil }

func (f *fakeConn) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	f.mu.Lock()
	f.sends = append(f.sends, req)
	f.mu.Unlock()
	return sampleTask(), nil
}

func (f *fakeConn) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	f.mu.Lock()
	f.sends = append(f.sends, req)
	f.mu.Unlock()
	if f.seq != nil {
		return f.seq(ctx, req)
	}
	return func(yield func(a2a.Event, error) bool) {}
}

func (f *fakeConn) SubscribeToTask(context.Context, string) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {}
}

func (f *fakeConn) lastSend() *a2a.SendMessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return nil
	}
	return f.sends[len(f.sends)-1]
}

func (f *fakeConn) getSends() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func sampleTask() *a2a.Task {
	return &a2a.Task{
		ID:        "t-1",
		ContextID: "c-1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted, Message: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("the answer"))},
		History: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("the question")),
			a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("the answer")),
		},
	}
}

// newTestApp returns an App wired to a fake connection (session armed
// the way a successful /connect would leave it).
func newTestApp(t *testing.T, conn *fakeConn) *App {
	t.Helper()
	a := New("", agent.Auto, nil)
	a.width, a.height = 100, 40
	a.layout()
	a.ready = true
	if conn == nil {
		conn = &fakeConn{}
	}
	a.session = agent.NewSession(conn)
	t.Cleanup(a.session.Shutdown)
	a.connState = "connected"
	return a
}

// update runs one message through Update, returning the model.
func update(t *testing.T, a *App, msg tea.Msg) tea.Cmd {
	t.Helper()
	m, cmd := a.Update(msg)
	if _, ok := m.(*App); !ok {
		t.Fatalf("Update returned %T", m)
	}
	return cmd
}

func rendered(a *App) string { return a.transcript.Render(a.transcriptV.Width, 0) }

func TestAgentEventsRenderTranscript(t *testing.T) {
	a := newTestApp(t, nil)
	before := a.transcript.Len()

	update(t, a, agentEventMsg{ev: agent.TaskUpdateEvent{
		TaskID: "t-1", ContextID: "c-1", State: a2a.TaskStateWorking, Source: "stream",
	}})
	update(t, a, agentEventMsg{ev: agent.AgentMessageEvent{
		Msg: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("hello from the agent")),
	}})
	update(t, a, agentEventMsg{ev: agent.ArtifactEvent{
		TaskID:   "t-1",
		Artifact: &a2a.Artifact{ID: "a-1", Name: "out.txt", Parts: a2a.ContentParts{a2a.NewTextPart("artifact line")}},
	}})
	// Completed replaces the working pill for the same task.
	update(t, a, agentEventMsg{ev: agent.TaskUpdateEvent{
		TaskID: "t-1", ContextID: "c-1", State: a2a.TaskStateCompleted, StatusText: "done",
	}})
	update(t, a, agentEventMsg{ev: agent.StreamDoneEvent{Op: "stream"}})

	out := rendered(a)
	if strings.Contains(out, "working") {
		// The completed pill must have replaced the working pill.
		t.Fatalf("working pill not replaced:\n%s", out)
	}
	if !strings.Contains(out, "hello from the agent") {
		t.Fatalf("agent text missing:\n%s", out)
	}
	if !strings.Contains(out, "out.txt") || !strings.Contains(out, "artifact line") {
		t.Fatalf("artifact block missing:\n%s", out)
	}
	if !strings.Contains(out, "completed") {
		t.Fatalf("completed pill missing:\n%s", out)
	}
	// The two state pills for t-1 must have replaced each other: exactly
	// one "task t-1" occurrence.
	if got := strings.Count(out, "task t-1"); got != 1 {
		t.Fatalf("task pill appears %d times, want 1:\n%s", got, out)
	}
	if a.transcript.Len() <= before {
		t.Fatal("transcript did not grow")
	}
	if a.inflight != 0 {
		t.Fatalf("inflight = %d after done", a.inflight)
	}
}

func TestInputRequiredContinuation(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)

	update(t, a, agentEventMsg{ev: agent.TaskUpdateEvent{
		TaskID: "t-in", ContextID: "c-in", State: a2a.TaskStateInputRequired, Source: "stream",
	}})
	if a.pending == nil || a.pending.taskID != "t-in" {
		t.Fatalf("pending input not tracked: %+v", a.pending)
	}
	out := rendered(a)
	if !strings.Contains(out, "input-required") || !strings.Contains(out, "reply to task t-in") {
		t.Fatalf("hint missing:\n%s", out)
	}

	// The next plain send must attach taskID + contextID.
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter}) // no-op: input empty
	a.input.SetValue("forty-two")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})

	deadline := time.After(2 * time.Second)
	for conn.getSends() == 0 {
		select {
		case <-deadline:
			t.Fatal("no send recorded")
		case <-time.After(5 * time.Millisecond):
		}
	}
	req := conn.lastSend()
	if req.Message.TaskID != a2a.TaskID("t-in") || req.Message.ContextID != "c-in" {
		t.Fatalf("continuation ids missing: %+v", req.Message)
	}
	if req.Message.Parts[0].Text() != "forty-two" {
		t.Fatalf("text = %q", req.Message.Parts[0].Text())
	}
	if req.Message.Metadata["a2uiRendererCapabilities"] == nil {
		t.Fatalf("capabilities metadata missing: %+v", req.Message.Metadata)
	}
	if a.pending != nil {
		t.Fatal("pending input not consumed")
	}
	if !strings.Contains(rendered(a), "forty-two") {
		t.Fatal("user echo missing from transcript")
	}
}

func TestStreamToggleCommand(t *testing.T) {
	a := newTestApp(t, nil)
	if a.streamMode {
		t.Fatal("default should follow card capabilities (fake says no streaming)")
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/stream on")})
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !a.streamMode {
		t.Fatal("/stream on did not stick")
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/stream off")})
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.streamMode {
		t.Fatal("/stream off did not stick")
	}
}

func TestWireToggleCommand(t *testing.T) {
	a := newTestApp(t, nil)
	if !a.wireLog.Enabled() {
		t.Fatal("wire capture should default on")
	}
	a.input.SetValue("/wire off")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.wireLog.Enabled() {
		t.Fatal("/wire off did not disable capture")
	}
	a.input.SetValue("/wire on")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !a.wireLog.Enabled() {
		t.Fatal("/wire on did not enable capture")
	}
}

func TestTaskCommandRendersHistory(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)

	a.input.SetValue("/task t-1")
	cmd := update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no command produced")
	}
	msg := cmd()
	res, ok := msg.(taskResultMsg)
	if !ok || res.task == nil {
		t.Fatalf("cmd produced %T", msg)
	}
	update(t, a, msg)

	out := rendered(a)
	for _, want := range []string{"completed", "the question", "the answer"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q missing:\n%s", want, out)
		}
	}
	if len(conn.gets) != 1 || conn.gets[0].ID != "t-1" {
		t.Fatalf("GetTask calls = %+v", conn.gets)
	}
}

func TestHistoryCommandPassesLength(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)
	a.input.SetValue("/history t-1 5")
	cmd := update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no command")
	}
	update(t, a, cmd())
	if len(conn.gets) != 1 {
		t.Fatalf("gets = %d", len(conn.gets))
	}
	hl := conn.gets[0].HistoryLength
	if hl == nil || *hl != 5 {
		t.Fatalf("historyLength = %v, want 5", hl)
	}
}

func TestCancelCommand(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.SetValue("/cancel t-1")
	cmd := update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no command")
	}
	msg := cmd()
	res, ok := msg.(taskResultMsg)
	if !ok || res.err == nil {
		t.Fatalf("expected cancel error result, got %#v", msg)
	}
	update(t, a, msg)
	if !strings.Contains(rendered(a), "TaskNotCancelable (-32002)") {
		t.Fatalf("friendly cancel error missing:\n%s", rendered(a))
	}
}

func TestStreamErrorFriendlyRendering(t *testing.T) {
	a := newTestApp(t, nil)
	a2aErr := a2a.NewError(a2a.ErrUnsupportedOperation, "streaming is not supported")
	update(t, a, agentEventMsg{ev: agent.StreamDoneEvent{Op: "stream", Err: a2aErr}})
	out := rendered(a)
	if !strings.Contains(out, "UnsupportedOperation (-32004)") {
		t.Fatalf("friendly name missing:\n%s", out)
	}
	if !strings.Contains(out, "UNSUPPORTED_OPERATION") {
		t.Fatalf("reason missing:\n%s", out)
	}
	if !strings.Contains(out, "streaming is not supported") {
		t.Fatalf("server message missing:\n%s", out)
	}
}

func TestEscCancelsActiveStream(t *testing.T) {
	conn := &fakeConn{}
	block := make(chan struct{})
	conn.seq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			info := a2a.TaskInfo{TaskID: "t-x", ContextID: "c-x"}
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
			case <-block:
			}
		}
	}
	a := newTestApp(t, conn)
	defer close(block)

	a.streamMode = true
	a.input.SetValue("hello")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.inflight != 1 {
		t.Fatalf("inflight = %d", a.inflight)
	}

	// Drain session events through Update until the working pill shows.
	deadline := time.After(2 * time.Second)
	for !strings.Contains(rendered(a), "working") {
		select {
		case ev := <-a.session.Events():
			update(t, a, agentEventMsg{ev: ev})
		case <-deadline:
			t.Fatalf("no working pill:\n%s", rendered(a))
		}
	}

	update(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	for a.inflight != 0 {
		select {
		case ev := <-a.session.Events():
			update(t, a, agentEventMsg{ev: ev})
		case <-time.After(2 * time.Second):
			t.Fatalf("stream not cancelled, inflight=%d", a.inflight)
		}
	}
	if !strings.Contains(a.statusText, "cancelled") {
		t.Fatalf("status = %q", a.statusText)
	}
}

// v1CardSrv serves a minimal v1.0 card whose JSONRPC interface points at
// the same server (JSON-RPC POSTs hit the handler too).
func v1CardSrv(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "agent-card.json") {
			card := fmt.Sprintf(`{
				"name": "CardAgent", "description": "test", "version": "1.0",
				"capabilities": {"streaming": true, "pushNotifications": false},
				"supportedInterfaces": [{"url": %q, "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}],
				"defaultInputModes": ["text/plain"], "defaultOutputModes": ["text/plain"],
				"skills": [{"id": "s1", "name": "echo", "description": "echo"}]
			}`, srv.URL)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, card)
			return
		}
		// JSON-RPC surface: answer every method with a completed task.
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("pong"))
		task := &a2a.Task{ID: "t-srv", ContextID: "c-srv", Status: a2a.TaskStatus{State: a2a.TaskStateCompleted, Message: msg}}
		result, _ := json.Marshal(a2a.StreamResponse{Event: task})
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "1", "result": json.RawMessage(result)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(frame)
	}))
	return srv
}

func TestConnectBuildsSessionAndSends(t *testing.T) {
	srv := v1CardSrv(t)
	defer srv.Close()

	a := New("", agent.Auto, nil)
	a.width, a.height = 100, 40
	a.layout()
	a.ready = true

	cmd := a.startConnect(srv.URL)
	msg := cmd()
	res, ok := msg.(connectResultMsg)
	if !ok {
		t.Fatalf("connect cmd produced %T", msg)
	}
	if res.err != nil || res.session == nil {
		t.Fatalf("connect failed: %v", res.err)
	}
	cmd = update(t, a, msg)
	if a.session == nil || a.connState != "connected" {
		t.Fatalf("session not attached (state %q)", a.connState)
	}
	if !a.streamMode {
		t.Fatal("stream default should follow card capabilities")
	}
	if a.session.Conn().BaseURL() != srv.URL {
		t.Fatalf("base URL = %q", a.session.Conn().BaseURL())
	}
	// The listener must be armed (and only once).
	if !a.listening || cmd == nil {
		t.Fatalf("listener not armed: listening=%v cmd=%v", a.listening, cmd != nil)
	}
	a.listening = false // test drives events manually

	// Blocking send through the full stack (connv1 + wirelog).
	a.streamMode = false
	a.input.SetValue("ping")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	deadline := time.After(3 * time.Second)
	for !strings.Contains(rendered(a), "pong") {
		select {
		case ev := <-a.session.Events():
			update(t, a, agentEventMsg{ev: ev})
		case <-deadline:
			t.Fatalf("no pong:\n%s", rendered(a))
		}
	}
	if a.wireLog.Last() == nil {
		t.Fatal("wire log captured nothing through connv1")
	}
	if !strings.Contains(string(a.wireLog.Last().ReqBody), "SendMessage") {
		t.Fatalf("wire frame missing method: %s", a.wireLog.Last().ReqBody)
	}
	a.session.Shutdown()
}

func TestConnectRejectsV03WithFriendlyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "agent.json") {
			_, _ = io.WriteString(w, `{"name":"Old","url":"http://x","protocolVersion":"0.3.0","capabilities":{}}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	a := New("", agent.Auto, nil)
	a.width, a.height = 100, 40
	a.layout()
	a.ready = true

	msg := a.startConnect(srv.URL)()
	res, ok := msg.(connectResultMsg)
	if !ok || res.err == nil {
		t.Fatalf("expected failure, got %#v", msg)
	}
	update(t, a, msg)
	if a.session != nil || a.connState != "error" {
		t.Fatalf("state = %q session=%v", a.connState, a.session != nil)
	}
	out := rendered(a)
	if !strings.Contains(out, "not implemented yet") {
		t.Fatalf("compat note missing:\n%s", out)
	}
}

func TestSanitizedAgentTextReachesTranscript(t *testing.T) {
	a := newTestApp(t, nil)
	dirty := "\x1b[31mred\x1b[0m\x07text\nwith a bell"
	update(t, a, agentEventMsg{ev: agent.AgentMessageEvent{
		Msg: a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(dirty)),
	}})
	out := rendered(a)
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\x07") {
		t.Fatalf("control bytes survived:\n%q", out)
	}
	if !strings.Contains(out, "redtext") {
		t.Fatalf("text mangled:\n%s", out)
	}
}

// TestClearCommand wipes the transcript.
func TestClearCommand(t *testing.T) {
	a := newTestApp(t, nil)
	a.addStatus("something to clear")
	a.input.SetValue("/clear")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.transcript.Len() != 0 {
		t.Fatalf("transcript not cleared: %d blocks", a.transcript.Len())
	}
}
