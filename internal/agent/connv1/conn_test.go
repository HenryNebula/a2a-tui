package connv1

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// fakeAgent is a hand-rolled v1.0 JSON-RPC agent. It records requests
// (method, A2A-Version header, decoded params) and answers from a
// scriptable per-method behavior.
type fakeAgent struct {
	mu       sync.Mutex
	methods  []string
	versions []string // A2A-Version header per request
	sends    []*a2a.SendMessageRequest
	getReqs  []a2a.GetTaskRequest
	stream   []a2a.Event // events for SendStreamingMessage / SubscribeToTask
	sendErr  *rpcError
	// sse signals streaming responses should be SSE framed; when false,
	// SendStreamingMessage answers like a plain request.
	sse bool
}

type rpcError struct {
	code    int
	message string
}

func newFakeAgent() *fakeAgent { return &fakeAgent{sse: true} }

func (f *fakeAgent) card(url string, streaming bool) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "fake",
		Description: "test agent",
		Version:     "1.0",
		Capabilities: a2a.AgentCapabilities{
			Streaming:         streaming,
			PushNotifications: true,
			ExtendedAgentCard: true,
		},
		DefaultInputModes:  []string{"text/plain"},
		DefaultOutputModes: []string{"text/plain"},
		SupportedInterfaces: []*a2a.AgentInterface{{
			URL:             url,
			ProtocolBinding: a2a.TransportProtocolJSONRPC,
			ProtocolVersion: a2a.Version,
		}},
		Skills: []a2a.AgentSkill{{ID: "echo", Name: "echo", Description: "echoes"}},
	}
}

type rpcRequest struct {
	ID     any             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.methods = append(f.methods, req.Method)
	f.versions = append(f.versions, r.Header.Get("A2A-Version"))
	f.mu.Unlock()
	writeResult := func(payload any) { writeResultWithID(w, req.ID, payload) }

	switch req.Method {
	case "SendMessage":
		f.recordSend(req.Params)
		if f.sendErr != nil {
			writeRPCError(w, req.ID, f.sendErr.code, f.sendErr.message)
			return
		}
		// SendMessage results ride the StreamResponse oneof wrapper.
		if len(f.stream) > 0 {
			writeResult(a2a.StreamResponse{Event: f.stream[0]})
			return
		}
		writeResult(a2a.StreamResponse{Event: f.defaultTask()})
	case "SendStreamingMessage":
		f.recordSend(req.Params)
		if f.sendErr != nil {
			if f.sse {
				writeSSEError(w, req.ID, f.sendErr)
				return
			}
			writeRPCError(w, req.ID, f.sendErr.code, f.sendErr.message)
			return
		}
		if !f.sse {
			writeResult(a2a.StreamResponse{Event: f.defaultTask()})
			return
		}
		writeSSE(w, req.ID, f.stream)
	case "SubscribeToTask":
		writeSSE(w, req.ID, f.stream)
	case "GetTask":
		var g a2a.GetTaskRequest
		_ = json.Unmarshal(req.Params, &g)
		f.mu.Lock()
		f.getReqs = append(f.getReqs, g)
		f.mu.Unlock()
		writeResult(f.defaultTask())
	case "CancelTask":
		writeResult(f.defaultTask())
	case "ListTasks":
		writeResult(&a2a.ListTasksResponse{Tasks: []*a2a.Task{f.defaultTask()}, TotalSize: 1})
	case "CreateTaskPushNotificationConfig":
		var cfg a2a.PushConfig
		_ = json.Unmarshal(req.Params, &cfg)
		writeResult(&cfg)
	case "ListTaskPushNotificationConfigs":
		writeResult(&a2a.ListTaskPushConfigResponse{Configs: []*a2a.PushConfig{{ID: "pc1", URL: "http://hook.example"}}})
	case "DeleteTaskPushNotificationConfig":
		writeResult(map[string]any{})
	case "GetExtendedAgentCard":
		writeResult(f.card(r.Host, true))
	default:
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (f *fakeAgent) recordSend(params json.RawMessage) {
	var req a2a.SendMessageRequest
	_ = json.Unmarshal(params, &req)
	f.mu.Lock()
	f.sends = append(f.sends, &req)
	f.mu.Unlock()
}

func (f *fakeAgent) defaultTask() *a2a.Task {
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("done"))
	return &a2a.Task{
		ID:        "t-1",
		ContextID: "c-1",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted, Message: msg},
		Artifacts: []*a2a.Artifact{{
			ID:   "a-1",
			Name: "report.txt",
			Parts: a2a.ContentParts{
				a2a.NewTextPart("line one\nline two\nline three\nline four\nline five\nline six\nline seven\nline eight\nline nine\nline ten\nline eleven\nline twelve\n"),
			},
		}},
	}
}

func (f *fakeAgent) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func (f *fakeAgent) lastSend() *a2a.SendMessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return nil
	}
	return f.sends[len(f.sends)-1]
}

func writeResultWithID(w http.ResponseWriter, id any, payload any) {
	result, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	resp := map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(result)}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func writeRPCError(w http.ResponseWriter, id any, code int, message string) {
	data, _ := json.Marshal([]map[string]any{{
		"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
		"reason": "TASK_NOT_FOUND",
		"domain": "a2a-protocol.org",
	}})
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message, "data": json.RawMessage(data)},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeSSE frames each event as a bare data: line holding a complete
// JSON-RPC response, then closes the stream (spec: no event names).
func writeSSE(w http.ResponseWriter, id any, events []a2a.Event) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	for _, ev := range events {
		result, err := json.Marshal(a2a.StreamResponse{Event: ev})
		if err != nil {
			return
		}
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(result)})
		_, _ = w.Write([]byte("data: " + string(frame) + "\n\n"))
		flusher.Flush()
	}
}

// writeSSEError delivers a JSON-RPC error object as a single SSE frame.
func writeSSEError(w http.ResponseWriter, id any, e *rpcError) {
	data, _ := json.Marshal([]map[string]any{{
		"@type":  "type.googleapis.com/google.rpc.ErrorInfo",
		"reason": "TASK_NOT_FOUND",
		"domain": "a2a-protocol.org",
	}})
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": e.code, "message": e.message, "data": json.RawMessage(data)},
	})
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("data: " + string(frame) + "\n\n"))
}

func newTestConn(t *testing.T, streaming bool) (*Conn, *fakeAgent) {
	t.Helper()
	fake := newFakeAgent()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	conn, err := New(context.Background(), fake.card(srv.URL, streaming), fake.card(srv.URL, streaming).SupportedInterfaces[0], nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return conn, fake
}

func userRequest(text string) *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text)),
	}
}

func TestSendMessageTaskResult(t *testing.T) {
	conn, fake := newTestConn(t, true)
	task := fake.defaultTask()
	fake.stream = []a2a.Event{task}

	res, err := conn.SendMessage(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	got, ok := res.(*a2a.Task)
	if !ok {
		t.Fatalf("want *a2a.Task, got %T", res)
	}
	if got.ID != "t-1" || got.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("bad task: %+v", got)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].Name != "report.txt" {
		t.Fatalf("bad artifacts: %+v", got.Artifacts)
	}
	if msg := got.Status.Message; msg == nil || msg.Parts[0].Text() != "done" {
		t.Fatalf("bad status message: %+v", msg)
	}
	if sends := fake.lastSend(); sends == nil || sends.Message.Parts[0].Text() != "hi" {
		t.Fatalf("send params not captured: %+v", sends)
	}
}

func TestSendMessageMessageResult(t *testing.T) {
	conn, fake := newTestConn(t, true)
	msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("hello there"))
	fake.stream = []a2a.Event{msg}

	res, err := conn.SendMessage(context.Background(), userRequest("hi"))
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if m, ok := res.(*a2a.Message); !ok || m.Parts[0].Text() != "hello there" {
		t.Fatalf("want agent message, got %#v", res)
	}
}

func TestSendStreamingSequence(t *testing.T) {
	conn, fake := newTestConn(t, true)
	working := a2a.NewStatusUpdateEvent(a2a.TaskInfo{TaskID: "t-9", ContextID: "c-9"}, a2a.TaskStateWorking, nil)
	artifact := a2a.NewArtifactEvent(a2a.TaskInfo{TaskID: "t-9", ContextID: "c-9"}, a2a.NewTextPart("chunk"))
	finale := a2a.NewStatusUpdateEvent(
		a2a.TaskInfo{TaskID: "t-9", ContextID: "c-9"},
		a2a.TaskStateCompleted,
		a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("all done")),
	)
	fake.stream = []a2a.Event{working, artifact, finale}

	var got []a2a.Event
	for ev, err := range conn.SendStreamingMessage(context.Background(), userRequest("hi")) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		got = append(got, ev)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	if s, ok := got[0].(*a2a.TaskStatusUpdateEvent); !ok || s.Status.State != a2a.TaskStateWorking {
		t.Fatalf("event 0 not working: %#v", got[0])
	}
	if a, ok := got[1].(*a2a.TaskArtifactUpdateEvent); !ok || a.TaskID != "t-9" {
		t.Fatalf("event 1 not artifact: %#v", got[1])
	}
	if s, ok := got[2].(*a2a.TaskStatusUpdateEvent); !ok || s.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("event 2 not completed: %#v", got[2])
	}
}

func TestStreamInputRequired(t *testing.T) {
	conn, fake := newTestConn(t, true)
	info := a2a.TaskInfo{TaskID: "t-in", ContextID: "c-in"}
	fake.stream = []a2a.Event{
		a2a.NewStatusUpdateEvent(info, a2a.TaskStateInputRequired, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("how many?"))),
	}
	var last a2a.Event
	for ev, err := range conn.SendStreamingMessage(context.Background(), userRequest("hi")) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		last = ev
	}
	s, ok := last.(*a2a.TaskStatusUpdateEvent)
	if !ok || s.Status.State != a2a.TaskStateInputRequired {
		t.Fatalf("want input-required, got %#v", last)
	}
}

func TestStreamError(t *testing.T) {
	conn, fake := newTestConn(t, true)
	fake.sendErr = &rpcError{code: -32001, message: "task not found"}

	var lastErr error
	for _, err := range conn.SendStreamingMessage(context.Background(), userRequest("hi")) {
		if err != nil {
			lastErr = err
		}
	}
	if lastErr == nil {
		t.Fatal("expected error from stream")
	}
	if !errors.Is(lastErr, a2a.ErrTaskNotFound) {
		t.Fatalf("want ErrTaskNotFound, got %v", lastErr)
	}
	if code := errorCode(lastErr); code != -32001 {
		t.Fatalf("code = %d, want -32001", code)
	}
}

// errorCode mirrors the SDK's code table for assertions.
func errorCode(err error) int {
	var a2aErr *a2a.Error
	if errors.As(err, &a2aErr) {
		for c, sentinel := range codeMap {
			if errors.Is(err, sentinel) {
				return c
			}
		}
	}
	return 0
}

var codeMap = map[int]error{
	-32001: a2a.ErrTaskNotFound,
	-32004: a2a.ErrUnsupportedOperation,
}

func TestA2AVersionHeader(t *testing.T) {
	conn, fake := newTestConn(t, true)
	if _, err := conn.SendMessage(context.Background(), userRequest("hi")); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.versions) == 0 || fake.versions[0] != "1.0" {
		t.Fatalf("A2A-Version header = %v, want [1.0]", fake.versions)
	}
	if len(fake.methods) == 0 || fake.methods[0] != "SendMessage" {
		t.Fatalf("method = %v", fake.methods)
	}
}

func TestStreamingFallbackWhenCardDeclaresNone(t *testing.T) {
	// Card says streaming=false: the SDK falls back to blocking SendMessage.
	conn, fake := newTestConn(t, false)
	fake.stream = nil // default task via SendMessage path
	for _, err := range conn.SendStreamingMessage(context.Background(), userRequest("hi")) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
	}
	calls := fake.calls()
	if len(calls) != 1 || calls[0] != "SendMessage" {
		t.Fatalf("calls = %v, want one SendMessage", calls)
	}
}

func TestTaskLifecycleMethods(t *testing.T) {
	conn, fake := newTestConn(t, true)

	n := 5
	task, err := conn.GetTask(context.Background(), "t-1", &n)
	if err != nil || task.ID != "t-1" {
		t.Fatalf("GetTask: %v %+v", err, task)
	}
	if hl := fake.getReqs[0].HistoryLength; hl == nil || *hl != 5 {
		t.Fatalf("historyLength not forwarded: %v", hl)
	}

	if _, err := conn.CancelTask(context.Background(), "t-1"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	list, err := conn.ListTasks(context.Background())
	if err != nil || len(list.Tasks) != 1 {
		t.Fatalf("ListTasks: %v %+v", err, list)
	}

	cfg, err := conn.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{URL: "http://hook.example", Token: "tok"})
	if err != nil || cfg.URL != "http://hook.example" {
		t.Fatalf("CreateTaskPushConfig: %v %+v", err, cfg)
	}

	configs, err := conn.ListTaskPushConfigs(context.Background(), "t-1")
	if err != nil || len(configs) != 1 || configs[0].ID != "pc1" {
		t.Fatalf("ListTaskPushConfigs: %v %+v", err, configs)
	}

	if err := conn.DeleteTaskPushConfig(context.Background(), "t-1", "pc1"); err != nil {
		t.Fatalf("DeleteTaskPushConfig: %v", err)
	}

	card, err := conn.GetExtendedAgentCard(context.Background())
	if err != nil || card.Name != "fake" {
		t.Fatalf("GetExtendedAgentCard: %v %+v", err, card)
	}

	want := []string{"GetTask", "CancelTask", "ListTasks", "CreateTaskPushNotificationConfig", "ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig", "GetExtendedAgentCard"}
	got := fake.calls()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
}

func TestSubscribeToTaskSnapshotFirst(t *testing.T) {
	conn, fake := newTestConn(t, true)
	info := a2a.TaskInfo{TaskID: "t-sub", ContextID: "c-sub"}
	snapshot := a2a.NewSubmittedTask(info, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("start")))
	fake.stream = []a2a.Event{
		snapshot,
		a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil),
	}

	var first a2a.Event
	count := 0
	for ev, err := range conn.SubscribeToTask(context.Background(), "t-sub") {
		if err != nil {
			t.Fatalf("subscribe error: %v", err)
		}
		if count == 0 {
			first = ev
		}
		count++
	}
	if task, ok := first.(*a2a.Task); !ok || task.ID != "t-sub" {
		t.Fatalf("first subscribe event should be task snapshot, got %#v", first)
	}
	if count != 2 {
		t.Fatalf("events = %d, want 2", count)
	}
}

func TestWireLogIntegration(t *testing.T) {
	fake := newFakeAgent()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	log := wirelog.New(0)
	card := fake.card(srv.URL, true)
	conn, err := New(context.Background(), card, card.SupportedInterfaces[0], nil, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.SendMessage(context.Background(), userRequest("hi")); err != nil {
		t.Fatal(err)
	}
	e := log.Last()
	if e == nil {
		t.Fatal("wirelog captured nothing")
	}
	if !strings.Contains(string(e.ReqBody), `"method":"SendMessage"`) {
		t.Fatalf("request frame not captured: %s", e.ReqBody)
	}
	if !strings.Contains(string(e.RespBody), `"task"`) {
		t.Fatalf("response frame not captured: %s", e.RespBody)
	}
}

func TestNewRejectsNonJSONRPC(t *testing.T) {
	fake := newFakeAgent()
	card := fake.card("http://x", true)
	card.SupportedInterfaces[0].ProtocolBinding = a2a.TransportProtocolHTTPJSON
	if _, err := New(context.Background(), card, card.SupportedInterfaces[0], nil, nil); err == nil {
		t.Fatal("expected error for non-JSONRPC interface")
	}
}
