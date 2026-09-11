package e2e

// The A2A 0.3 leg: a hand-rolled legacy server (static card + scripted
// message/send, message/stream, tasks/get, tasks/cancel, tasks/resubscribe)
// driven through the same Resolve → NewConn → Session stack as the 1.0
// fixture, pinning the dispatch and 0.3↔1.0 translation end to end. The
// server is intentionally separate from internal/compat03's test fakes: it
// owns real task state and streams with pacing instead of replaying static
// bodies.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// --- wire shapes (legacy 0.3 JSON) ---------------------------------------

type wire03Status struct {
	State     string         `json:"state"`
	Message   *wire03Message `json:"message,omitempty"`
	Timestamp string         `json:"timestamp,omitempty"`
}

type wire03Part struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
}

type wire03Message struct {
	Role      string       `json:"role"`
	Parts     []wire03Part `json:"parts"`
	MessageID string       `json:"messageId,omitempty"`
	TaskID    string       `json:"taskId,omitempty"`
	ContextID string       `json:"contextId,omitempty"`
	Kind      string       `json:"kind,omitempty"`
}

type wire03Task struct {
	ID        string       `json:"id"`
	ContextID string       `json:"contextId"`
	Status    wire03Status `json:"status"`
	Kind      string       `json:"kind"`
}

type wire03StatusUpdate struct {
	TaskID    string       `json:"taskId"`
	ContextID string       `json:"contextId"`
	Kind      string       `json:"kind"`
	Status    wire03Status `json:"status"`
	Final     bool         `json:"final"`
}

// fake03 is the scripted 0.3 agent: an in-memory task store plus method
// handlers that answer from it. "long" messages hold their stream open in
// working until tasks/cancel lands, which is what the cancel scenario needs.
type fake03 struct {
	mu      sync.Mutex
	tasks   map[string]*fake03Task
	calls   []string
	counter int
}

type fake03Task struct {
	id       string
	ctxID    string
	state    string // submitted | working | completed | canceled
	message  string
	userText string
}

func newFake03() *fake03 { return &fake03{tasks: map[string]*fake03Task{}} }

// start serves the fake on an ephemeral port.
func (f *fake03) start(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(f.handler(func() string { return srv.URL }))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fake03) handler(base func() string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/agent-card.json", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil) // no 1.x card: auto-detect must fall through
	})
	mux.HandleFunc("/.well-known/agent.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"name":"e2e 0.3 agent","description":"scripted legacy agent","url":%q,`+
			`"version":"1.0","protocolVersion":"0.3.0",`+
			`"capabilities":{"streaming":true,"pushNotifications":false},`+
			`"defaultInputModes":["text"],"defaultOutputModes":["text"],"skills":[]}`, base())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, nil)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Message struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"message"`
				ID string `json:"id"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, req.Method)
		f.mu.Unlock()

		switch req.Method {
		case "message/send":
			task := f.createTask(userText(req.Params.Message.Parts))
			f.setState(task.id, "completed", "0.3 says: "+task.userText)
			writeJSON(w, req.ID, f.snapshot(task.id))
		case "message/stream":
			f.stream(w, req.ID, userText(req.Params.Message.Parts))
		case "tasks/get":
			if f.task(req.Params.ID) == nil {
				writeErr(w, req.ID, -32001, "Task not found: "+req.Params.ID)
				return
			}
			writeJSON(w, req.ID, f.snapshot(req.Params.ID))
		case "tasks/cancel":
			if f.task(req.Params.ID) == nil {
				writeErr(w, req.ID, -32001, "Task not found: "+req.Params.ID)
				return
			}
			f.setState(req.Params.ID, "canceled", "canceled by client")
			writeJSON(w, req.ID, f.snapshot(req.Params.ID))
		case "tasks/resubscribe":
			task := f.task(req.Params.ID)
			if task == nil {
				writeErr(w, req.ID, -32001, "Task not found: "+req.Params.ID)
				return
			}
			f.flushStream(w, req.ID, func(send func(any, bool)) {
				send(f.snapshot(req.Params.ID), false) // full snapshot first
				send(updateFor(task, "completed", "resubscribe final", true), true)
			})
		default:
			writeErr(w, req.ID, -32601, "Method not found: "+req.Method)
		}
	})
	return mux
}

// stream implements message/stream: snapshot → working → (a cancel-aware
// hold for "long" messages) → terminal final.
func (f *fake03) stream(w http.ResponseWriter, id json.RawMessage, text string) {
	task := f.createTask(text)
	f.flushStream(w, id, func(send func(any, bool)) {
		send(f.snapshot(task.id), false) // task snapshot, submitted
		f.setState(task.id, "working", "")
		send(updateFor(task, "working", "crunching", false), false)
		if text == "long" {
			// Hold the stream in working until cancellation lands.
			deadline := time.Now().Add(30 * time.Second)
			for {
				st := f.stateOf(task.id)
				if st != "working" || time.Now().After(deadline) {
					send(updateFor(task, st, "canceled by client", true), true)
					return
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		f.setState(task.id, "completed", "0.3 streamed: "+text)
		send(updateFor(task, "completed", "0.3 streamed: "+text, true), true)
	})
}

// flushStream writes SSE frames produced by script.
func (f *fake03) flushStream(w http.ResponseWriter, id json.RawMessage, script func(send func(any, bool))) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	send := func(result any, _ bool) {
		payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
		if err != nil {
			return
		}
		_, _ = w.Write(append([]byte("data: "), append(payload, '\n', '\n')...))
		if flusher != nil {
			flusher.Flush()
		}
	}
	script(send)
}

// task accessors (the store is shared across request goroutines: stream
// handlers and tasks/cancel mutate the same task concurrently, so all task
// field access goes through f.mu).

func (f *fake03) createTask(userText string) *fake03Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counter++
	task := &fake03Task{
		id:       fmt.Sprintf("t03-%d", f.counter),
		ctxID:    fmt.Sprintf("c03-%d", f.counter),
		state:    "submitted",
		userText: userText,
	}
	f.tasks[task.id] = task
	return task
}

func (f *fake03) task(id string) *fake03Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tasks[id]
}

// setState mutates a task's state and status message under the lock.
func (f *fake03) setState(id, state, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[id]; t != nil {
		t.state, t.message = state, message
	}
}

// stateOf reads a task's state under the lock.
func (f *fake03) stateOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t := f.tasks[id]; t != nil {
		return t.state
	}
	return ""
}

// snapshot renders a task's current wire form under the lock.
func (f *fake03) snapshot(id string) wire03Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tasks[id]
	if t == nil {
		return wire03Task{ID: id, Status: wire03Status{State: "unknown"}, Kind: "task"}
	}
	task := wire03Task{
		ID:        t.id,
		ContextID: t.ctxID,
		Status:    wire03Status{State: t.state, Timestamp: "2026-09-11T00:00:00Z"},
		Kind:      "task",
	}
	if t.message != "" {
		task.Status.Message = replyFor(t, t.message)
	}
	return task
}

func (f *fake03) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.calls {
		if m == method {
			return true
		}
	}
	return false
}

// updateFor builds a status-update event for a task. Only the task's
// immutable fields are read (state and message are passed in).
func updateFor(t *fake03Task, state, message string, final bool) wire03StatusUpdate {
	return wire03StatusUpdate{
		TaskID:    t.id,
		ContextID: t.ctxID,
		Kind:      "status-update",
		Status:    wire03Status{State: state, Message: replyFor(t, message)},
		Final:     final,
	}
}

// replyFor builds the agent reply message for a task.
func replyFor(t *fake03Task, text string) *wire03Message {
	return &wire03Message{
		Role:      "agent",
		Parts:     []wire03Part{{Kind: "text", Text: text}},
		MessageID: "m-" + t.id,
		TaskID:    t.id,
		ContextID: t.ctxID,
	}
}

// writeJSON answers a blocking JSON-RPC call.
func writeJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id), "result": result})
}

// writeErr answers with a legacy JSON-RPC error envelope.
func writeErr(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(id),
		"error":   map[string]any{"code": code, "message": message},
	})
}

func userText(parts []struct {
	Text string `json:"text"`
}) string {
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, " ")
}

// --- scenarios -------------------------------------------------------------

func TestV03AutoDetectAndBlockingSend(t *testing.T) {
	fake := newFake03()
	srv := fake.start(t)
	s := connect(t, srv.URL)

	if got := s.Conn().WireVersion(); got != agent.WireV03 {
		t.Fatalf("auto-detect picked %s, want 0.3 (the 1.x card path 404s)", got)
	}

	s.Send(context.Background(), "hello legacy", agent.SendOptions{})
	match, seen := collectUntil(t, s, "blocking 0.3 send", func(ev agent.Event) bool {
		return isDone(ev, "send")
	})
	if match == nil {
		return
	}
	updates := taskUpdates(seen)
	if len(updates) == 0 || updates[len(updates)-1].State != a2a.TaskStateCompleted {
		t.Fatalf("final state wrong: %s", summarize(seen))
	}
	if got := updates[len(updates)-1].StatusText; !strings.Contains(got, "0.3 says: hello legacy") {
		t.Fatalf("status text = %q (seen %s)", got, summarize(seen))
	}
	if !fake.called("message/send") {
		t.Fatal("message/send never reached the server")
	}
}

func TestV03StreamToTerminal(t *testing.T) {
	fake := newFake03()
	srv := fake.start(t)
	s := connect(t, srv.URL)

	s.SendStreaming(context.Background(), "stream me", agent.SendOptions{})
	_, seen := collectUntil(t, s, "0.3 stream to terminal", func(ev agent.Event) bool {
		return isDone(ev, "stream")
	})

	var states []a2a.TaskState
	for _, u := range taskUpdates(seen) {
		states = append(states, u.State)
	}
	if len(states) < 3 || states[0] != a2a.TaskStateSubmitted ||
		states[len(states)-1] != a2a.TaskStateCompleted {
		t.Fatalf("state sequence = %v (seen %s)", states, summarize(seen))
	}
	sawWorking := false
	for _, st := range states {
		if st == a2a.TaskStateWorking {
			sawWorking = true
		}
	}
	if !sawWorking {
		t.Fatalf("no working state in %v", states)
	}
}

func TestV03Resubscribe(t *testing.T) {
	fake := newFake03()
	srv := fake.start(t)
	s := connect(t, srv.URL)

	s.Send(context.Background(), "subscribe to me", agent.SendOptions{})
	// NB: the blocking-send path publishes task events but does not feed
	// the session registry (a known upstream gap — stream and push paths
	// do), so the task ID comes from the update event itself.
	_, seen := collectUntil(t, s, "blocking send done", func(ev agent.Event) bool {
		return isDone(ev, "send")
	})
	id := ""
	for _, ev := range seen {
		if u, ok := ev.(agent.TaskUpdateEvent); ok && u.Source == "send" {
			id = u.TaskID
		}
	}
	if id == "" {
		t.Fatalf("blocking send published no task update: %s", summarize(seen))
	}

	s.Subscribe(context.Background(), id)
	match, _ := collectUntil(t, s, "resubscribe snapshot", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.Source == "subscribe"
	})
	if match == nil {
		return
	}
	snap := match.(agent.TaskUpdateEvent)
	if snap.Snapshot == nil || string(snap.Snapshot.ID) != id {
		t.Fatalf("first resubscribe event is not a task snapshot: %+v", snap)
	}
	if !fake.called("tasks/resubscribe") {
		t.Fatal("tasks/resubscribe never reached the server")
	}
	// The final:true frame ends the subscription cleanly.
	collectUntil(t, s, "subscribe done", func(ev agent.Event) bool {
		return isDone(ev, "subscribe")
	})
}

func TestV03CancelMidStream(t *testing.T) {
	fake := newFake03()
	srv := fake.start(t)
	s := connect(t, srv.URL)

	s.SendStreaming(context.Background(), "long", agent.SendOptions{})
	match, _ := collectUntil(t, s, "long task working", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.State == a2a.TaskStateWorking
	})
	if match == nil {
		return
	}
	id := match.(agent.TaskUpdateEvent).TaskID

	task, err := s.Conn().CancelTask(context.Background(), id)
	if err != nil {
		t.Fatalf("tasks/cancel: %v", err)
	}
	if task.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("cancel result state = %s", task.Status.State)
	}
	// The held-open stream translates the canceled update and the session
	// ends the pump at the terminal state.
	collectUntil(t, s, "canceled over the stream", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.TaskID == id && u.State == a2a.TaskStateCanceled
	})
	if !fake.called("tasks/cancel") {
		t.Fatal("tasks/cancel never reached the server")
	}
}
