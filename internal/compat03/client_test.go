package compat03

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
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// fakeAgent is a hand-rolled A2A 0.3 server. It records every request
// (method, headers, decoded params) and answers from per-method
// responders, so tests both verify what the client sends and drive the
// full response surface.
type fakeAgent struct {
	mu       sync.Mutex
	requests []recordedRequest
	respond  map[string]responder
}

// recordedRequest is one JSON-RPC call as seen by the server.
type recordedRequest struct {
	method      string
	contentType string
	accept      string
	a2aVersion  bool // true when the (1.x-only) A2A-Version header was sent
	body        []byte
	params      map[string]any
}

// responder answers one JSON-RPC call.
type responder func(w http.ResponseWriter, r *http.Request, params map[string]any)

func newFakeAgent() *fakeAgent {
	return &fakeAgent{respond: map[string]responder{}}
}

func (f *fakeAgent) on(method string, fn responder) *fakeAgent {
	f.respond[method] = fn
	return f
}

// replyJSON returns a responder writing a raw JSON-RPC envelope with
// the given HTTP status.
func replyJSON(status int, envelope string) responder {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, envelope)
	}
}

// replySSE returns a responder streaming a raw event-stream body.
func replySSE(body string) responder {
	return func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var env struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	var params map[string]any
	if len(env.Params) > 0 {
		_ = json.Unmarshal(env.Params, &params)
	}
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{
		method:      env.Method,
		contentType: r.Header.Get("Content-Type"),
		accept:      r.Header.Get("Accept"),
		a2aVersion:  r.Header.Get("A2A-Version") != "",
		body:        body,
		params:      params,
	})
	fn, found := f.respond[env.Method]
	f.mu.Unlock()

	if !found {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		id, _ := json.Marshal(env.ID)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":`+string(id)+`,"error":{"code":-32601,"message":"Method not found"}}`)
		return
	}
	fn(w, r, params)
}

// lastRequest returns the most recent recorded request for method.
func (f *fakeAgent) lastRequest(method string) *recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.requests) - 1; i >= 0; i-- {
		if f.requests[i].method == method {
			return &f.requests[i]
		}
	}
	return nil
}

func (f *fakeAgent) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, req := range f.requests {
		if req.method == method {
			n++
		}
	}
	return n
}

// newTestClient wires a Client to a fake agent server.
func newTestClient(t *testing.T, fake *fakeAgent) *Client {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client, err := New(context.Background(), srv.URL, &CardV03{
		Name:            "Fake 0.3",
		URL:             srv.URL,
		ProtocolVersion: "0.3.0",
		Capabilities:    CapabilitiesV03{Streaming: true, PushNotifications: true},
	}, srv.Client(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

func pongRequest() *a2a.SendMessageRequest {
	return &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("Reply with the single word: pong.")),
	}
}

func TestClientSendTaskResult(t *testing.T) {
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-task-result.json"))))
	c := newTestClient(t, fake)

	res, err := c.SendMessage(context.Background(), pongRequest())
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		t.Fatalf("result type %T, want task", res)
	}
	if string(task.ID) != "225d6247-06ba-4cda-a08b-33ae35c8dcfa" || task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("task = %+v", task)
	}

	req := fake.lastRequest(methodSend)
	if req == nil {
		t.Fatal("no message/send recorded")
	}
	if req.contentType != "application/json" {
		t.Errorf("Content-Type = %q", req.contentType)
	}
	if req.a2aVersion {
		t.Error("0.3 client must not send the A2A-Version header")
	}
	msg, _ := req.params["message"].(map[string]any)
	if msg == nil || msg["role"] != "user" {
		t.Fatalf("params.message = %#v", req.params)
	}
	parts, _ := msg["parts"].([]any)
	if len(parts) != 1 {
		t.Fatalf("parts = %#v", parts)
	}
	if part, _ := parts[0].(map[string]any); part["kind"] != "text" || part["text"] != "Reply with the single word: pong." {
		t.Errorf("part = %#v", parts[0])
	}
	if id, _ := msg["messageId"].(string); id == "" {
		t.Error("messageId must be generated when missing")
	}
}

func TestClientSendMessageResult(t *testing.T) {
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	c := newTestClient(t, fake)

	res, err := c.SendMessage(context.Background(), pongRequest())
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	msg, ok := res.(*a2a.Message)
	if !ok {
		t.Fatalf("result type %T, want message", res)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text() != "pong" {
		t.Errorf("message = %+v", msg)
	}
}

func TestClientSendOmitsConfigWhenAbsent(t *testing.T) {
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	c := newTestClient(t, fake)

	if _, err := c.SendMessage(context.Background(), pongRequest()); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := fake.lastRequest(methodSend)
	if _, present := sent.params["configuration"]; present {
		t.Errorf("configuration sent although the request had none: %#v", sent.params["configuration"])
	}
}

func TestClientSendMapsConfig(t *testing.T) {
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	c := newTestClient(t, fake)

	hist := 3
	req := pongRequest()
	req.Config = &a2a.SendMessageConfig{
		AcceptedOutputModes: []string{"text/plain"},
		HistoryLength:       &hist,
	}
	if _, err := c.SendMessage(context.Background(), req); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := fake.lastRequest(methodSend)
	cfg, _ := sent.params["configuration"].(map[string]any)
	if cfg == nil {
		t.Fatal("configuration not sent")
	}
	if modes, _ := cfg["acceptedOutputModes"].([]any); len(modes) != 1 || modes[0] != "text/plain" {
		t.Errorf("acceptedOutputModes = %#v", cfg["acceptedOutputModes"])
	}
	if hl, _ := cfg["historyLength"].(float64); hl != 3 {
		t.Errorf("historyLength = %#v", cfg["historyLength"])
	}
	if blocking, _ := cfg["blocking"].(bool); !blocking {
		t.Errorf("blocking = %#v, want true (ReturnImmediately=false inverts to blocking)", cfg["blocking"])
	}
}

func TestClientStreamLifecycle(t *testing.T) {
	fake := newFakeAgent().on(methodStream, replySSE(string(loadFixture(t, "stream.sse"))))
	c := newTestClient(t, fake)

	var kinds []string
	var states []a2a.TaskState
	var texts []string
	for ev, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch e := ev.(type) {
		case *a2a.Task:
			kinds = append(kinds, "task")
			states = append(states, e.Status.State)
		case *a2a.TaskStatusUpdateEvent:
			kinds = append(kinds, "status-update")
			states = append(states, e.Status.State)
			if e.Status.Message != nil {
				texts = append(texts, e.Status.Message.Parts[0].Text())
			}
		case *a2a.TaskArtifactUpdateEvent:
			kinds = append(kinds, "artifact-update")
			if e.Artifact != nil {
				texts = append(texts, e.Artifact.Parts[0].Text())
			}
		}
	}

	wantKinds := []string{"task", "status-update", "artifact-update", "artifact-update", "status-update"}
	if strings.Join(kinds, ",") != strings.Join(wantKinds, ",") {
		t.Errorf("event kinds = %v, want %v", kinds, wantKinds)
	}
	wantStates := []a2a.TaskState{a2a.TaskStateSubmitted, a2a.TaskStateWorking, a2a.TaskStateCompleted}
	if len(states) != len(wantStates) {
		t.Fatalf("states = %v, want %v", states, wantStates)
	}
	for i := range wantStates {
		if states[i] != wantStates[i] {
			t.Errorf("state[%d] = %v, want %v", i, states[i], wantStates[i])
		}
	}
	if len(texts) != 3 || texts[0] != "po" || texts[1] != "ng" || texts[2] != "pong" {
		t.Errorf("texts = %v, want [po ng pong]", texts)
	}
	if fake.count(methodStream) != 1 {
		t.Errorf("message/stream called %d times, want 1", fake.count(methodStream))
	}

	req := fake.lastRequest(methodStream)
	if !strings.Contains(req.accept, "text/event-stream") {
		t.Errorf("Accept = %q, want text/event-stream", req.accept)
	}
	if req.a2aVersion {
		t.Error("0.3 stream must not send A2A-Version")
	}
}

func TestClientStreamEndsOnFinalOnly(t *testing.T) {
	// final:true arrives on input-required (non-terminal in 1.x); the
	// 0.3 client must still end the sequence after it.
	fake := newFakeAgent().on(methodStream, replySSE(string(loadFixture(t, "input-required.sse"))))
	c := newTestClient(t, fake)

	n := 0
	for ev, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		n++
		su, ok := ev.(*a2a.TaskStatusUpdateEvent)
		if !ok || su.Status.State != a2a.TaskStateInputRequired {
			t.Fatalf("event %d = %#v", n, ev)
		}
	}
	if n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

func TestClientSubscribeResubscribe(t *testing.T) {
	fake := newFakeAgent().on(methodResubscribe, replySSE(string(loadFixture(t, "stream.sse"))))
	c := newTestClient(t, fake)

	n := 0
	for ev, err := range c.SubscribeToTask(context.Background(), "225d6247-06ba-4cda-a08b-33ae35c8dcfa") {
		if err != nil {
			t.Fatalf("subscribe error: %v", err)
		}
		if ev == nil {
			t.Fatal("nil event")
		}
		n++
	}
	if n != 5 {
		t.Errorf("events = %d, want 5", n)
	}
	req := fake.lastRequest(methodResubscribe)
	if req.params["id"] != "225d6247-06ba-4cda-a08b-33ae35c8dcfa" {
		t.Errorf("params = %#v, want id", req.params)
	}
}

func TestClientGetAndCancelTask(t *testing.T) {
	fake := newFakeAgent().
		on(methodGetTask, replyJSON(http.StatusOK, string(loadFixture(t, "send-task-result.json")))).
		on(methodCancelTask, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"1","result":{"id":"t1","contextId":"c1","status":{"state":"canceled"},"kind":"task"}}`))
	c := newTestClient(t, fake)

	hist := 10
	task, err := c.GetTask(context.Background(), "t1", &hist)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if string(task.ID) != "225d6247-06ba-4cda-a08b-33ae35c8dcfa" {
		t.Errorf("task id = %q", task.ID)
	}
	if req := fake.lastRequest(methodGetTask); req.params["historyLength"] != float64(10) {
		t.Errorf("historyLength = %#v", req.params["historyLength"])
	}

	if _, err := c.GetTask(context.Background(), "t1", nil); err != nil {
		t.Fatalf("GetTask(nil history): %v", err)
	}
	if req := fake.lastRequest(methodGetTask); req.params["historyLength"] != nil {
		t.Errorf("historyLength should be omitted, params = %#v", req.params)
	}

	task, err = c.CancelTask(context.Background(), "t1")
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if task.Status.State != a2a.TaskStateCanceled {
		t.Errorf("state = %v", task.Status.State)
	}
}

func TestClientUnsupportedMethods(t *testing.T) {
	c := newTestClient(t, newFakeAgent())
	if _, err := c.ListTasks(context.Background()); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Errorf("ListTasks err = %v, want ErrUnsupportedOperation", err)
	}
	if _, err := c.GetExtendedAgentCard(context.Background()); !errors.Is(err, a2a.ErrUnsupportedOperation) {
		t.Errorf("GetExtendedAgentCard err = %v, want ErrUnsupportedOperation", err)
	}
}

func TestClientPushConfigLifecycle(t *testing.T) {
	fake := newFakeAgent().
		on(methodPushSet, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"1","result":{"taskId":"t1","pushNotificationConfig":{"id":"cfg-9","url":"https://client/hook","token":"tok","authentication":{"schemes":["Bearer"],"credentials":"s"}}}}`)).
		on(methodPushGet, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"2","result":{"taskId":"t1","pushNotificationConfig":{"id":"cfg-9","url":"https://client/hook"}}}`)).
		on(methodPushList, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"3","result":[{"taskId":"t1","pushNotificationConfig":{"id":"cfg-9","url":"https://client/hook"}},{"taskId":"t1","pushNotificationConfig":{"id":"cfg-2","url":"https://other/hook"}}]}`)).
		on(methodPushDelete, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"4","result":null}`))
	c := newTestClient(t, fake)

	got, err := c.CreateTaskPushConfig(context.Background(), &a2a.PushConfig{
		TaskID: "t1",
		URL:    "https://client/hook",
		Token:  "tok",
		Auth:   &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "s"},
	})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got.ID != "cfg-9" || got.Auth == nil || got.Auth.Scheme != "Bearer" {
		t.Errorf("set result = %+v", got)
	}
	if req := fake.lastRequest(methodPushSet); req.params["taskId"] != "t1" {
		t.Errorf("set params = %#v", req.params)
	}

	got1, err := c.GetTaskPushConfig(context.Background(), "t1", "cfg-9")
	if err != nil || got1.ID != "cfg-9" {
		t.Fatalf("get: %+v %v", got1, err)
	}

	list, err := c.ListTaskPushConfigs(context.Background(), "t1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[1].ID != "cfg-2" {
		t.Errorf("list = %+v", list)
	}

	if err := c.DeleteTaskPushConfig(context.Background(), "t1", "cfg-9"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if req := fake.lastRequest(methodPushDelete); req.params["pushNotificationConfigId"] != "cfg-9" {
		t.Errorf("delete params = %#v", req.params)
	}
}

func TestClientErrorMapping(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		message  string
		sentinel error
	}{
		{"task not found", -32001, "Task not found: t1", a2a.ErrTaskNotFound},
		{"not cancelable", -32002, "task already completed", a2a.ErrTaskNotCancelable},
		{"push unsupported", -32003, "no push", a2a.ErrPushNotificationNotSupported},
		{"unsupported operation", -32004, "streaming not supported", a2a.ErrUnsupportedOperation},
		{"content type", -32005, "modes rejected", a2a.ErrUnsupportedContentType},
		{"method not found", -32601, "Method not found", a2a.ErrMethodNotFound},
		{"parse", -32700, "parse error", a2a.ErrParseError},
		{"server", -32000, "boom", a2a.ErrServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      "1",
				"error":   map[string]any{"code": tc.code, "message": tc.message},
			})
			fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(body)))
			c := newTestClient(t, fake)

			_, err := c.SendMessage(context.Background(), pongRequest())
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tc.sentinel) {
				t.Errorf("errors.Is(%v, sentinel) = false", err)
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("error %q does not carry server message %q", err, tc.message)
			}
		})
	}

	t.Run("unknown code", func(t *testing.T) {
		fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"1","error":{"code":-32099,"message":"weird"}}`))
		c := newTestClient(t, fake)
		_, err := c.SendMessage(context.Background(), pongRequest())
		if err == nil || !strings.Contains(err.Error(), "-32099") || !strings.Contains(err.Error(), "weird") {
			t.Errorf("err = %v, want code and message preserved", err)
		}
	})

	t.Run("http error with json body", func(t *testing.T) {
		fake := newFakeAgent().on(methodSend, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(loadFixture(t, "humanbrowser-unauthorized-error.json"))
		})
		c := newTestClient(t, fake)
		_, err := c.SendMessage(context.Background(), pongRequest())
		if err == nil || !strings.Contains(err.Error(), "Unauthorized") {
			t.Errorf("err = %v, want server message", err)
		}
	})

	t.Run("http error with non-json body", func(t *testing.T) {
		fake := newFakeAgent().on(methodSend, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>bad gateway</html>")
		})
		c := newTestClient(t, fake)
		_, err := c.SendMessage(context.Background(), pongRequest())
		if err == nil || !strings.Contains(err.Error(), "502") {
			t.Errorf("err = %v, want status", err)
		}
	})

	t.Run("null send result", func(t *testing.T) {
		fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, `{"jsonrpc":"2.0","id":"1","result":null}`))
		c := newTestClient(t, fake)
		_, err := c.SendMessage(context.Background(), pongRequest())
		if !errors.Is(err, a2a.ErrInvalidAgentResponse) {
			t.Errorf("err = %v, want ErrInvalidAgentResponse", err)
		}
	})
}

func TestClientStreamServerErrors(t *testing.T) {
	t.Run("stream rejected with 32004 json body", func(t *testing.T) {
		fake := newFakeAgent().on(methodStream, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","error":{"code":-32004,"message":"streaming not supported"}}`)
		})
		c := newTestClient(t, fake)
		var events, errs int
		for _, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
			if err != nil {
				if !errors.Is(err, a2a.ErrUnsupportedOperation) {
					t.Fatalf("err = %v, want ErrUnsupportedOperation", err)
				}
				errs++
				continue
			}
			events++
		}
		if events != 0 || errs == 0 {
			t.Errorf("events = %d, errs = %d", events, errs)
		}
	})

	t.Run("stream rejected with 401 json body", func(t *testing.T) {
		fake := newFakeAgent().on(methodStream, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write(loadFixture(t, "humanbrowser-unauthorized-error.json"))
		})
		c := newTestClient(t, fake)
		for _, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
			if err != nil {
				if !strings.Contains(err.Error(), "Unauthorized") {
					t.Fatalf("err = %v, want Unauthorized", err)
				}
				break
			}
			t.Fatal("expected immediate error event")
		}
	})

	t.Run("stream frame error mid-stream", func(t *testing.T) {
		fake := newFakeAgent().on(methodStream, replySSE(
			"data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"taskId\":\"t\",\"contextId\":\"c\",\"status\":{\"state\":\"working\"},\"kind\":\"status-update\",\"final\":false}}\n\n"+
				"data: not-json\n\n"))
		c := newTestClient(t, fake)
		var events int
		var lastErr error
		for ev, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
			if err != nil {
				lastErr = err
				break
			}
			events++
			_ = ev
		}
		if events != 1 || lastErr == nil || !strings.Contains(lastErr.Error(), "malformed") {
			t.Errorf("events = %d, err = %v", events, lastErr)
		}
	})

	t.Run("stream rpc error frame", func(t *testing.T) {
		fake := newFakeAgent().on(methodStream, replySSE(
			"data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":-32001,\"message\":\"Task not found\"}}\n\n"))
		c := newTestClient(t, fake)
		for _, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
			if err != nil {
				if !errors.Is(err, a2a.ErrTaskNotFound) {
					t.Fatalf("err = %v, want ErrTaskNotFound", err)
				}
				break
			}
		}
	})
}

func TestClientStreamCancellation(t *testing.T) {
	// A stream that never closes; only ctx cancellation ends it.
	fake := newFakeAgent().on(methodStream, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		<-r.Context().Done()
	})
	c := newTestClient(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var streamErr error
		for _, err := range c.SendStreamingMessage(ctx, pongRequest()) {
			if err != nil {
				streamErr = err
				break
			}
		}
		done <- streamErr
	}()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("expected error after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not stop the stream")
	}
}

func TestClientStreamIgnoresNullResultFrames(t *testing.T) {
	fake := newFakeAgent().on(methodStream, replySSE(
		"data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":null}\n\n"+
			"data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{\"taskId\":\"t\",\"contextId\":\"c\",\"status\":{\"state\":\"completed\"},\"final\":true,\"kind\":\"status-update\"}}\n\n"))
	c := newTestClient(t, fake)
	var events int
	for ev, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if ev != nil {
			events++
		}
	}
	if events != 1 {
		t.Errorf("events = %d, want 1 (null frame skipped)", events)
	}
}

func TestClientStreamIdleWatchdogOnRealBody(t *testing.T) {
	// Issue #37: real stdlib response bodies (HTTP/1 bodyEOFSignal,
	// HTTP/2 transportResponseBody) carry no read deadline, so the old
	// deadline fast path never engaged and a stream that fell silent
	// hung forever. The watchdog must end it over real HTTP too.
	shortenIdleTimeout(t, 150*time.Millisecond)

	fake := newFakeAgent().on(methodStream, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":null}\n\n")
		flusher.Flush()
		<-r.Context().Done() // then fall silent for good
	})
	c := newTestClient(t, fake)

	done := make(chan error, 1)
	go func() {
		var streamErr error
		for _, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
			if err != nil {
				streamErr = err
				break
			}
		}
		done <- streamErr
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "idle") {
			t.Fatalf("err = %v, want idle timeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("silent stream was not unblocked by the idle watchdog")
	}
}

func TestReadCappedRejectsOversize(t *testing.T) {
	// Issue #38: readCapped used to hand back the truncated bytes, which
	// then failed downstream as a confusing JSON "unexpected EOF". The
	// cap must fail here, explicitly.
	big := strings.Repeat("a", maxResponseBytes+1)
	if _, err := readCapped(strings.NewReader(big)); !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("err = %v, want errResponseTooLarge", err)
	}
	if _, err := readCapped(strings.NewReader(strings.Repeat("a", maxResponseBytes))); err != nil {
		t.Fatalf("exact-cap body should pass: %v", err)
	}
	data, err := readCapped(strings.NewReader(`{"ok":true}`))
	if err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("data = %q, err = %v", data, err)
	}
}

func TestClientOversizedResponseErrors(t *testing.T) {
	// The full body is exactly one byte over the cap, so the oversize is
	// detected without leaving the server mid-write.
	prefix := `{"jsonrpc":"2.0","id":"1","result":"`
	suffix := `"}`
	oversized := prefix + strings.Repeat("x", maxResponseBytes+1-len(prefix)-len(suffix)) + suffix

	fake := newFakeAgent().on(methodSend, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, oversized)
	})
	c := newTestClient(t, fake)
	_, err := c.SendMessage(context.Background(), pongRequest())
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("err = %v, want errResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "message/send") {
		t.Errorf("error lacks method context: %v", err)
	}
	if strings.Contains(err.Error(), "unexpected EOF") {
		t.Errorf("truncated-body decode error leaked: %v", err)
	}
}

func TestClientOversizedStreamRejectionBodyErrors(t *testing.T) {
	// Same cap, exercised through the non-SSE rejection path of a
	// streaming call.
	fake := newFakeAgent().on(methodStream, func(w http.ResponseWriter, r *http.Request, _ map[string]any) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
	})
	c := newTestClient(t, fake)
	for _, err := range c.SendStreamingMessage(context.Background(), pongRequest()) {
		if err == nil {
			t.Fatal("expected an error event")
		}
		if !errors.Is(err, errResponseTooLarge) || !strings.Contains(err.Error(), "message/stream") {
			t.Fatalf("err = %v, want capped error with method context", err)
		}
		break
	}
}

func TestClientWireLogTransport(t *testing.T) {
	log := wirelog.New(16)
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client, err := New(context.Background(), srv.URL, nil, srv.Client(), log)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.SendMessage(context.Background(), pongRequest()); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	entries := log.Snapshot()
	if len(entries) == 0 {
		t.Fatal("wire log captured nothing")
	}
	if !strings.Contains(string(entries[0].ReqBody), "message/send") {
		t.Errorf("captured request = %s", entries[0].ReqBody)
	}
}

func TestNewRejectsBadURL(t *testing.T) {
	for _, bad := range []string{"", "ftp://x", "not a url", "/relative"} {
		if _, err := New(context.Background(), bad, nil, nil, nil); err == nil {
			t.Errorf("New(%q) should fail", bad)
		}
	}
}

func TestClientNilCardWorks(t *testing.T) {
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client, err := New(context.Background(), srv.URL, nil, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if client.Card() != nil || client.CardV03() != nil {
		t.Error("card accessors should be nil")
	}
	if client.Capabilities().Streaming {
		t.Error("nil card means no capabilities")
	}
	if _, err := client.SendMessage(context.Background(), pongRequest()); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
}

func TestClientMetadataPassthrough(t *testing.T) {
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	c := newTestClient(t, fake)

	req := pongRequest()
	req.Metadata = map[string]any{"a2uiRendererCapabilities": map[string]any{"v1.0": map[string]any{"supportedCatalogIds": []string{}}}}
	if _, err := c.SendMessage(context.Background(), req); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := fake.lastRequest(methodSend)
	meta, _ := sent.params["metadata"].(map[string]any)
	if meta == nil {
		t.Fatalf("metadata not sent: %#v", sent.params)
	}
	if _, ok := meta["a2uiRendererCapabilities"]; !ok {
		t.Errorf("metadata = %#v", meta)
	}
}

func TestClientSendDataParts(t *testing.T) {
	// A2UI rides in data parts; they must reach the agent as 0.3 data
	// parts with objects intact.
	fake := newFakeAgent().on(methodSend, replyJSON(http.StatusOK, string(loadFixture(t, "send-message-result.json"))))
	c := newTestClient(t, fake)

	req := pongRequest()
	req.Message.Parts = append(req.Message.Parts,
		a2a.NewDataPart(map[string]any{"version": "0.9", "action": map[string]any{"name": "submit"}}))
	if _, err := c.SendMessage(context.Background(), req); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	sent := fake.lastRequest(methodSend)
	msg, _ := sent.params["message"].(map[string]any)
	parts, _ := msg["parts"].([]any)
	if len(parts) != 2 {
		t.Fatalf("parts = %#v", parts)
	}
	data, _ := parts[1].(map[string]any)
	obj, _ := data["data"].(map[string]any)
	if obj == nil || obj["version"] != "0.9" {
		t.Errorf("data part = %#v", data)
	}
}
