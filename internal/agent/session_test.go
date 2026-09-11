package agent

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/fixtureagent"
)

// fakeConn is an in-memory AgentConn used to drive the session pumps.
type fakeConn struct {
	mu sync.Mutex

	sends     []*a2a.SendMessageRequest
	pushCfgs  []*a2a.PushConfig
	pushErr   error
	streamSeq func(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
	subscribe func(ctx context.Context, id string) iter.Seq2[a2a.Event, error]
	destroyed int
}

func (f *fakeConn) Card() *a2a.AgentCard     { return nil }
func (f *fakeConn) CardSummary() CardSummary { return CardSummary{} }
func (f *fakeConn) WireVersion() string      { return WireV1 }
func (f *fakeConn) BaseURL() string          { return "http://fake" }
func (f *fakeConn) ListTasks(context.Context) (*a2a.ListTasksResponse, error) {
	return &a2a.ListTasksResponse{}, nil
}
func (f *fakeConn) GetTask(_ context.Context, id string, _ *int) (*a2a.Task, error) {
	return nil, a2a.ErrTaskNotFound
}
func (f *fakeConn) CancelTask(context.Context, string) (*a2a.Task, error) { return nil, nil }
func (f *fakeConn) CreateTaskPushConfig(_ context.Context, cfg *a2a.PushConfig) (*a2a.PushConfig, error) {
	f.mu.Lock()
	f.pushCfgs = append(f.pushCfgs, cfg)
	err := f.pushErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
func (f *fakeConn) ListTaskPushConfigs(context.Context, string) ([]*a2a.PushConfig, error) {
	return nil, nil
}
func (f *fakeConn) DeleteTaskPushConfig(context.Context, string, string) error { return nil }
func (f *fakeConn) GetExtendedAgentCard(context.Context) (*a2a.AgentCard, error) {
	return nil, errors.New("no extended card")
}
func (f *fakeConn) Destroy() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroyed++
	return nil
}

func (f *fakeConn) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	f.mu.Lock()
	f.sends = append(f.sends, req)
	f.mu.Unlock()
	// Blocking send answers with a completed task carrying a message.
	info := a2a.TaskInfo{TaskID: "t-block", ContextID: "c-block"}
	return a2a.NewSubmittedTask(info, req.Message), nil
}

func (f *fakeConn) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	f.mu.Lock()
	f.sends = append(f.sends, req)
	f.mu.Unlock()
	if f.streamSeq != nil {
		return f.streamSeq(ctx, req)
	}
	return nil
}

func (f *fakeConn) SubscribeToTask(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
	if f.subscribe != nil {
		return f.subscribe(ctx, id)
	}
	return nil
}

func (f *fakeConn) lastSend() *a2a.SendMessageRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) == 0 {
		return nil
	}
	return f.sends[len(f.sends)-1]
}

func (f *fakeConn) pushConfigCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pushCfgs)
}

func (f *fakeConn) lastPushConfig() *a2a.PushConfig {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pushCfgs) == 0 {
		return nil
	}
	return f.pushCfgs[len(f.pushCfgs)-1]
}

// drain reads session events until pred matches, returning the matching
// event and everything seen before it.
func drain(s *Session, pred func(Event) bool, timeout time.Duration) (Event, []Event) {
	deadline := time.After(timeout)
	var seen []Event
	for {
		select {
		case ev := <-s.Events():
			seen = append(seen, ev)
			if pred(ev) {
				return ev, seen
			}
		case <-deadline:
			return nil, seen
		}
	}
}

func isStreamDone(ev Event) bool {
	d, ok := ev.(StreamDoneEvent)
	return ok && d.Op == "stream"
}

func TestSendStreamingHappyPath(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-1", ContextID: "c-1"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			events := []a2a.Event{
				a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil),
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("hello ")),
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("world")),
				a2a.NewArtifactEvent(info, a2a.NewTextPart("chunk 1")),
				a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("done"))),
			}
			for _, e := range events {
				if !yield(e, nil) {
					return
				}
			}
		}
	}
	s := NewSession(conn)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})

	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no StreamDoneEvent; seen=%v", seen)
	}
	if done.(StreamDoneEvent).Err != nil {
		t.Fatalf("unexpected done err: %v", done.(StreamDoneEvent).Err)
	}

	var states []a2a.TaskState
	var messages int
	var artifacts int
	for _, ev := range seen {
		switch e := ev.(type) {
		case TaskUpdateEvent:
			states = append(states, e.State)
		case AgentMessageEvent:
			messages++
		case ArtifactEvent:
			artifacts++
		}
	}
	if len(states) != 2 || states[0] != a2a.TaskStateWorking || states[1] != a2a.TaskStateCompleted {
		t.Fatalf("task states = %v", states)
	}
	if messages != 3 { // 2 stream messages + final status message
		t.Fatalf("messages = %d, want 3", messages)
	}
	if artifacts != 1 {
		t.Fatalf("artifacts = %d", artifacts)
	}

	// Registry must have been fed from the same events.
	meta, ok := s.Registry().Get("t-1")
	if !ok {
		t.Fatal("task t-1 not registered")
	}
	if meta.State != a2a.TaskStateCompleted || meta.Terminal() != true {
		t.Fatalf("registry meta = %+v", meta)
	}
}

func TestSendBlockingTaskResult(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()

	s.Send(context.Background(), "hi", SendOptions{})

	done, seen := drain(s, func(ev Event) bool {
		d, ok := ev.(StreamDoneEvent)
		return ok && d.Op == "send"
	}, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	var update TaskUpdateEvent
	for _, ev := range seen {
		if u, ok := ev.(TaskUpdateEvent); ok {
			update = u
		}
	}
	if update.TaskID != "t-block" || update.Snapshot == nil {
		t.Fatalf("bad task update: %+v", update)
	}

	req := conn.lastSend()
	if req == nil {
		t.Fatal("no request recorded")
	}
	if req.Message.Role != a2a.MessageRoleUser || req.Message.Parts[0].Text() != "hi" {
		t.Fatalf("bad message: %+v", req.Message)
	}
	if req.Message.TaskID != "" || req.Message.ContextID != "" {
		t.Fatalf("continuation ids leaked into fresh send: %+v", req.Message)
	}
	// A2UI renderer capabilities metadata must ride on every message.
	if req.Message.Metadata["a2uiRendererCapabilities"] == nil {
		t.Fatalf("a2ui capabilities missing: %+v", req.Message.Metadata)
	}
}

func TestSendContinuationAndMetadata(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()

	extra := map[string]any{"custom": 1}
	s.Send(context.Background(), "reply", SendOptions{TaskID: "t-in", ContextID: "c-in", Metadata: extra})
	drain(s, func(ev Event) bool {
		d, ok := ev.(StreamDoneEvent)
		return ok && d.Op == "send"
	}, 2*time.Second)

	req := conn.lastSend()
	if req.Message.TaskID != a2a.TaskID("t-in") || req.Message.ContextID != "c-in" {
		t.Fatalf("continuation ids missing: %+v", req.Message)
	}
	if req.Message.Metadata["custom"] != 1 {
		t.Fatalf("extra metadata missing: %+v", req.Message.Metadata)
	}
	if req.Message.Metadata["a2uiRendererCapabilities"] == nil {
		t.Fatal("capabilities metadata missing")
	}
}

// TestSendRawSendsPrebuiltMessage covers the A2UI action path: a
// caller-built message (data part + own context) is sent as-is with the
// engine metadata merged on top; explicit options still override.
func TestSendRawSendsPrebuiltMessage(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()

	part := a2a.NewDataPart(map[string]any{"version": "v1.0", "action": map[string]any{"name": "submit"}})
	part.Metadata = map[string]any{"mimeType": "application/a2ui+json"}
	msg := a2a.NewMessage(a2a.MessageRoleUser, part)
	msg.ContextID = "c-a2ui"
	s.SendRaw(context.Background(), msg, SendOptions{})
	drain(s, func(ev Event) bool {
		d, ok := ev.(StreamDoneEvent)
		return ok && d.Op == "send"
	}, 2*time.Second)

	req := conn.lastSend()
	if req.Message.Role != a2a.MessageRoleUser || len(req.Message.Parts) != 1 {
		t.Fatalf("prebuilt message altered: %+v", req.Message)
	}
	if mt, _ := req.Message.Parts[0].Metadata["mimeType"].(string); mt != "application/a2ui+json" {
		t.Fatalf("part metadata lost: %v", req.Message.Parts[0].Metadata)
	}
	if req.Message.ContextID != "c-a2ui" {
		t.Fatalf("message context id lost: %q", req.Message.ContextID)
	}
	if req.Message.Metadata["a2uiRendererCapabilities"] == nil {
		t.Fatalf("capabilities metadata not merged: %+v", req.Message.Metadata)
	}

	// Options override the message's own continuation IDs.
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {}
	}
	msg2 := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("again"))
	msg2.ContextID = "c-original"
	s.SendRawStreaming(context.Background(), msg2, SendOptions{ContextID: "c-override"})
	drain(s, isStreamDone, 2*time.Second)
	req2 := conn.lastSend()
	if req2.Message.ContextID != "c-override" {
		t.Fatalf("stream context id = %q, want override", req2.Message.ContextID)
	}
}

func TestStreamBackpressureCompacts(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-flood", ContextID: "c"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			// Flood first (overflows the channel while nobody reads),
			// then the terminal state so the pump ends.
			for i := 0; i < eventBufferSize+200; i++ {
				if !yield(a2a.NewArtifactEvent(info, a2a.NewTextPart("x")), nil) {
					return
				}
			}
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, nil), nil)
		}
	}
	s := NewSession(conn)

	s.SendStreaming(context.Background(), "hi", SendOptions{})

	// Do not read for a moment so the channel fills and drops happen.
	time.Sleep(150 * time.Millisecond)

	var compacted *StreamCompactedEvent
	var done Event
	deadline := time.After(3 * time.Second)
	for done == nil {
		select {
		case ev := <-s.Events():
			switch e := ev.(type) {
			case StreamCompactedEvent:
				compacted = &e
			case StreamDoneEvent:
				done = ev
			}
		case <-deadline:
			t.Fatalf("timed out waiting for done; compacted=%v", compacted)
		}
	}
	if compacted == nil || compacted.Dropped == 0 {
		t.Fatalf("expected compaction report, got %+v", compacted)
	}
	s.Shutdown()
}

func TestCancelMidStream(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-slow", ContextID: "c"}
	block := make(chan struct{})
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
			case <-block:
				yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, nil), nil)
			}
		}
	}
	s := NewSession(conn)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})
	if _, seen := drain(s, func(ev Event) bool {
		_, ok := ev.(TaskUpdateEvent)
		return ok
	}, 2*time.Second); len(seen) == 0 {
		t.Fatal("pump produced no events")
	}

	if n := s.CancelActive(); n != 1 {
		t.Fatalf("CancelActive = %d, want 1", n)
	}
	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done after cancel; seen=%v", seen)
	}
	if !errors.Is(done.(StreamDoneEvent).Err, context.Canceled) {
		t.Fatalf("done err = %v, want context.Canceled", done.(StreamDoneEvent).Err)
	}
	close(block)
}

func TestSubscribeSnapshotFirst(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-sub", ContextID: "c-sub"}
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			snapshot := a2a.NewSubmittedTask(info, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("start")))
			if !yield(snapshot, nil) {
				return
			}
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			// End on a terminal state: a non-terminal end would (correctly)
			// trigger the resubscribe loop.
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, nil), nil)
		}
	}
	s := NewSession(conn)
	defer s.Shutdown()

	s.Subscribe(context.Background(), "t-sub")

	var first Event
	done, seen := drain(s, func(ev Event) bool {
		if _, ok := ev.(StreamDoneEvent); ok {
			return true
		}
		if first == nil {
			first = ev
		}
		return false
	}, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	u, ok := first.(TaskUpdateEvent)
	if !ok || u.Snapshot == nil || u.TaskID != "t-sub" {
		t.Fatalf("first subscribe event should be a task snapshot, got %#v", first)
	}
	if _, ok := s.Registry().Get("t-sub"); !ok {
		t.Fatal("subscribed task not registered")
	}
}

func TestStreamErrorEndsPump(t *testing.T) {
	conn := &fakeConn{}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(nil, a2a.ErrTaskNotFound)
		}
	}
	s := NewSession(conn)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})
	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	if !errors.Is(done.(StreamDoneEvent).Err, a2a.ErrTaskNotFound) {
		t.Fatalf("done err = %v", done.(StreamDoneEvent).Err)
	}
}

func TestShutdownExitsGoroutines(t *testing.T) {
	conn := &fakeConn{}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			<-ctx.Done()
			yield(nil, ctx.Err())
		}
	}
	s := NewSession(conn)
	s.SendStreaming(context.Background(), "hi", SendOptions{})
	time.Sleep(50 * time.Millisecond) // let the pump block

	finished := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(finished)
	}()
	s.Shutdown()
	select {
	case <-finished:
	case <-time.After(shutdownGrace + time.Second):
		t.Fatal("pump goroutines did not exit on Shutdown")
	}
	if conn.destroyed != 1 {
		t.Fatalf("conn destroyed %d times", conn.destroyed)
	}
	// Shutdown is idempotent.
	s.Shutdown()
	if conn.destroyed != 1 {
		t.Fatalf("conn destroyed %d times after second Shutdown", conn.destroyed)
	}
}

func TestRegistryObservation(t *testing.T) {
	r := NewRegistry()
	info := a2a.TaskInfo{TaskID: "t-r", ContextID: "c-r"}
	r.Observe(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil))
	r.Observe(a2a.NewArtifactEvent(info, a2a.NewTextPart("data")))
	r.Observe(a2a.NewArtifactUpdateEvent(info, "a-1", a2a.NewTextPart("more")))

	m, ok := r.Get("t-r")
	if !ok {
		t.Fatal("missing task")
	}
	if m.State != a2a.TaskStateWorking || m.ContextID != "c-r" {
		t.Fatalf("meta = %+v", m)
	}
	if m.Artifacts != 1 { // update events must not recount
		t.Fatalf("artifacts = %d, want 1", m.Artifacts)
	}

	// Titles stick to the first text seen.
	info2 := a2a.TaskInfo{TaskID: "t-t", ContextID: "c"}
	r.Observe(a2a.NewStatusUpdateEvent(info2, a2a.TaskStateSubmitted,
		a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("first title"))))
	r.Observe(a2a.NewStatusUpdateEvent(info2, a2a.TaskStateCompleted,
		a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("second title"))))
	m2, _ := r.Get("t-t")
	if m2.Title != "first title" || m2.State != a2a.TaskStateCompleted {
		t.Fatalf("title/state = %+v", m2)
	}

	if n := r.Count(); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

// ---------------------------------------------------------------------------
// Stream reconnect
// ---------------------------------------------------------------------------

// fastReconnect shortens a session's backoff so reconnect tests run in
// milliseconds.
func fastReconnect(s *Session, attempts int) *Session {
	s.reconnectInitial = time.Millisecond
	s.reconnectMax = 2 * time.Millisecond
	s.reconnectAttempts = attempts
	return s
}

func TestStreamReconnectsAfterEarlyEnd(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-r", ContextID: "c-r"}

	// The stream yields one working update, then simply ends (server closed
	// the SSE connection early). No error, no terminal state.
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil)
		}
	}
	// The resubscribe head is a full Task snapshot still in working (the
	// dedupe case), followed by the completing update.
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			snap := &a2a.Task{ID: a2a.TaskID(id), ContextID: "c-r",
				Status: a2a.TaskStatus{State: a2a.TaskStateWorking}}
			if !yield(snap, nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted,
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("done"))), nil)
		}
	}
	s := fastReconnect(NewSession(conn), 5)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})

	var reconnect *ReconnectingEvent
	working, completed := 0, 0
	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	if done.(StreamDoneEvent).Err != nil {
		t.Fatalf("done err = %v", done.(StreamDoneEvent).Err)
	}
	for _, ev := range seen {
		switch e := ev.(type) {
		case ReconnectingEvent:
			r := e
			reconnect = &r
		case TaskUpdateEvent:
			switch e.State {
			case a2a.TaskStateWorking:
				working++
			case a2a.TaskStateCompleted:
				completed++
			}
		}
	}
	if reconnect == nil || reconnect.TaskID != "t-r" || reconnect.Attempt != 1 {
		t.Fatalf("reconnecting event = %+v", reconnect)
	}
	if working != 1 {
		t.Fatalf("working pills = %d, want 1 (snapshot must dedupe); events=%v", working, seen)
	}
	if completed != 1 {
		t.Fatalf("completed pills = %d, want 1", completed)
	}
}

func TestStreamReconnectsAfterError(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-e", ContextID: "c"}
	boom := errors.New("connection reset")
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			yield(nil, boom)
		}
	}
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			snap := &a2a.Task{ID: a2a.TaskID(id), Status: a2a.TaskStatus{State: a2a.TaskStateCompleted}}
			yield(snap, nil)
		}
	}
	s := fastReconnect(NewSession(conn), 5)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})

	var reconnect *ReconnectingEvent
	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	if done.(StreamDoneEvent).Err != nil {
		t.Fatalf("done err = %v", done.(StreamDoneEvent).Err)
	}
	for _, ev := range seen {
		if r, ok := ev.(ReconnectingEvent); ok {
			reconnect = &r
		}
	}
	if reconnect == nil || reconnect.Err == nil {
		t.Fatalf("reconnecting event should carry the cause: %+v", reconnect)
	}
}

func TestStreamReconnectBoundedAttempts(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-g", ContextID: "c"}
	boom := errors.New("network down")
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			yield(nil, boom)
		}
	}
	// Every resubscribe keeps failing.
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(nil, boom)
		}
	}
	s := fastReconnect(NewSession(conn), 3)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})

	attempts, gaveUp := 0, false
	done, seen := drain(s, isStreamDone, 5*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	for _, ev := range seen {
		switch e := ev.(type) {
		case ReconnectingEvent:
			attempts++
		case ErrorEvent:
			if strings.Contains(e.Err.Error(), "gave up reconnecting") {
				gaveUp = true
			}
		}
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if !gaveUp {
		t.Fatalf("no give-up error; seen=%v", seen)
	}
	if done.(StreamDoneEvent).Err == nil {
		t.Fatal("done should carry the give-up error")
	}
}

func TestStreamReconnectStoppedByCancel(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-c", ContextID: "c"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil)
		}
	}
	subCalls := 0
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {}
	}
	_ = subCalls
	s := fastReconnect(NewSession(conn), 100)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})
	// Let the first stream end and one reconnect cycle start, then cancel.
	if _, seen := drain(s, func(ev Event) bool {
		_, ok := ev.(ReconnectingEvent)
		return ok
	}, 2*time.Second); len(seen) == 0 {
		t.Fatal("no reconnect attempt before cancel")
	}
	if n := s.CancelActive(); n != 1 {
		t.Fatalf("CancelActive = %d, want 1", n)
	}
	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done after cancel; seen=%v", seen)
	}
	if !errors.Is(done.(StreamDoneEvent).Err, context.Canceled) {
		t.Fatalf("done err = %v, want context.Canceled", done.(StreamDoneEvent).Err)
	}
}

func TestStreamFatalErrorSkipsReconnect(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-f", ContextID: "c"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			yield(nil, a2a.ErrTaskNotFound)
		}
	}
	subscribed := false
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		subscribed = true
		return func(yield func(a2a.Event, error) bool) {}
	}
	s := fastReconnect(NewSession(conn), 5)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})
	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	if !errors.Is(done.(StreamDoneEvent).Err, a2a.ErrTaskNotFound) {
		t.Fatalf("done err = %v", done.(StreamDoneEvent).Err)
	}
	for _, ev := range seen {
		if _, ok := ev.(ReconnectingEvent); ok {
			t.Fatal("TaskNotFound must not trigger reconnect attempts")
		}
	}
	if subscribed {
		t.Fatal("SubscribeToTask must not be called for a fatal error")
	}
}

// ---------------------------------------------------------------------------
// Push notifications
// ---------------------------------------------------------------------------

// pushToken reads the session's generated push token (test helper; the
// token is unexported state).
func pushToken(s *Session) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushToken
}

func TestPushRegistersConfigForNewTasks(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-p", ContextID: "c-p"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewSubmittedTask(info, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("push"))), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, nil), nil)
		}
	}
	s := fastReconnect(NewSession(conn), 5)
	defer s.Shutdown()

	url, err := s.StartPush("")
	if err != nil {
		t.Fatalf("StartPush: %v", err)
	}
	if !strings.Contains(url, "/push") {
		t.Fatalf("push URL = %q", url)
	}
	s.SetPushEnabled(true)
	if !s.PushEnabled() {
		t.Fatal("push should be enabled")
	}

	s.SendStreaming(context.Background(), "push", SendOptions{})
	if _, seen := drain(s, isStreamDone, 2*time.Second); seen == nil {
		t.Fatal("no events")
	}

	deadline := time.After(2 * time.Second)
	for conn.pushConfigCount() == 0 {
		select {
		case <-deadline:
			t.Fatal("no push config registered for the new task")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cfg := conn.lastPushConfig()
	if string(cfg.TaskID) != "t-p" {
		t.Fatalf("config task = %q, want t-p", cfg.TaskID)
	}
	if cfg.URL != url || cfg.Token == "" {
		t.Fatalf("config url/token = %q/%q, want %q/<non-empty>", cfg.URL, cfg.Token, url)
	}
	// Exactly one registration per task even as more events arrive.
	s.SendStreaming(context.Background(), "push", SendOptions{})
	drain(s, isStreamDone, 2*time.Second)
	time.Sleep(50 * time.Millisecond)
	if n := conn.pushConfigCount(); n != 1 {
		t.Fatalf("push configs = %d, want 1 (no duplicates per task)", n)
	}
}

func TestPushDisabledByDefaultAndNoServer(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()
	if s.PushEnabled() || s.PushURL() != "" {
		t.Fatal("push must default off with no server")
	}
	info := a2a.TaskInfo{TaskID: "t-n", ContextID: "c"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewSubmittedTask(info, nil), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, nil), nil)
		}
	}
	s.SendStreaming(context.Background(), "hi", SendOptions{})
	drain(s, isStreamDone, 2*time.Second)
	if n := conn.pushConfigCount(); n != 0 {
		t.Fatalf("push configs = %d, want 0 while push is off", n)
	}
}

func TestPushUnsupportedAutoDisables(t *testing.T) {
	conn := &fakeConn{}
	conn.pushErr = a2a.NewError(a2a.ErrPushNotificationNotSupported, "no push")
	info := a2a.TaskInfo{TaskID: "t-u", ContextID: "c"}
	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewSubmittedTask(info, nil), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateCompleted, nil), nil)
		}
	}
	s := NewSession(conn)
	defer s.Shutdown()

	if _, err := s.StartPush(""); err != nil {
		t.Fatalf("StartPush: %v", err)
	}
	s.SetPushEnabled(true)
	s.SendStreaming(context.Background(), "hi", SendOptions{})

	note, seen := drain(s, func(ev Event) bool {
		l, ok := ev.(StatusLineEvent)
		return ok && strings.Contains(l.Text, "not supported")
	}, 2*time.Second)
	if note == nil {
		t.Fatalf("no friendly unsupported note; seen=%v", seen)
	}
	if s.PushEnabled() {
		t.Fatal("push should auto-disable after PushNotificationNotSupported")
	}
}

func TestPushWebhookDeliversStampedEvents(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()

	url, err := s.StartPush("")
	if err != nil {
		t.Fatalf("StartPush: %v", err)
	}
	tok := pushToken(s)
	body := `{"statusUpdate": {"taskId": "t-w", "contextId": "c-w",
		"status": {"state": "TASK_STATE_WORKING"}}}`
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("webhook POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("webhook status = %d", resp.StatusCode)
	}

	upd, seen := drain(s, func(ev Event) bool {
		_, ok := ev.(TaskUpdateEvent)
		return ok
	}, 2*time.Second)
	if upd == nil {
		t.Fatalf("no task update from webhook; seen=%v", seen)
	}
	u := upd.(TaskUpdateEvent)
	if u.Source != "push" || u.TaskID != "t-w" || u.State != a2a.TaskStateWorking {
		t.Fatalf("update = %+v", u)
	}
	var hasStatusLine bool
	for _, ev := range seen {
		if l, ok := ev.(StatusLineEvent); ok && strings.Contains(l.Text, "⇄ push") {
			hasStatusLine = true
		}
	}
	if !hasStatusLine {
		t.Fatalf("no ⇄ push status line; seen=%v", seen)
	}
	if _, ok := s.Registry().Get("t-w"); !ok {
		t.Fatal("webhook event did not feed the registry")
	}

	// StopPush tears the server down and clears the URL.
	s.StopPush()
	if s.PushURL() != "" {
		t.Fatal("PushURL should be empty after StopPush")
	}
	if r, err := http.Post(url, "application/json", strings.NewReader(body)); err == nil {
		_ = r.Body.Close()
		t.Fatal("webhook should be closed after StopPush")
	}
}

// TestPushEndToEndThroughFixtureAgent drives the full push pipeline against
// the in-process fixture agent (real SDK server): enable push, send the
// "push" keyword over the stream, and assert at least one event arrives
// through the WEBHOOK path (Source:"push").
func TestPushEndToEndThroughFixtureAgent(t *testing.T) {
	ts, err := fixtureagent.StartTest()
	if err != nil {
		t.Fatalf("fixture: %v", err)
	}
	defer ts.Close()

	res, err := Resolve(t.Context(), &http.Client{Timeout: 5 * time.Second}, ts.URL, Auto)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	conn, err := NewConn(t.Context(), res, nil)
	if err != nil {
		t.Fatalf("NewConn: %v", err)
	}
	s := fastReconnect(NewSession(conn), 5)
	defer s.Shutdown()

	pushURL, err := s.StartPush("")
	if err != nil {
		t.Fatalf("StartPush: %v", err)
	}
	s.SetPushEnabled(true)

	s.SendStreaming(t.Context(), "push", SendOptions{})

	var pushEvent *TaskUpdateEvent
	var streamDone bool
	deadline := time.After(10 * time.Second)
	for pushEvent == nil {
		select {
		case ev := <-s.Events():
			switch e := ev.(type) {
			case TaskUpdateEvent:
				if e.Source == "push" {
					pushEvent = &e
				}
			case StreamDoneEvent:
				streamDone = true
				// Push copies can race past the stream's own completion;
				// keep reading until the webhook copy lands.
			case ErrorEvent:
				t.Fatalf("unexpected error event: %v", e.Err)
			}
		case <-deadline:
			t.Fatalf("no push-delivered event within 10s (streamDone=%v)", streamDone)
		}
	}
	if pushEvent.TaskID == "" {
		t.Fatalf("push event without task id: %+v", pushEvent)
	}
	if _, ok := s.Registry().Get(pushEvent.TaskID); !ok {
		t.Fatalf("push task %q missing from registry", pushEvent.TaskID)
	}
	if !strings.HasPrefix(pushURL, "http://127.0.0.1:") {
		t.Fatalf("push URL = %q", pushURL)
	}
}

// TestStreamInteractiveStateEndsCleanly: a stream that parks the task in
// input-required and closes cleanly must NOT trigger the resubscribe loop —
// the agent's turn is over, it is waiting for the client.
func TestStreamInteractiveStateEndsCleanly(t *testing.T) {
	conn := &fakeConn{}
	info := a2a.TaskInfo{TaskID: "t-i", ContextID: "c-i"}

	conn.streamSeq = func(ctx context.Context, _ *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if !yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateInputRequired,
				a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("name?"))), nil)
		}
	}
	subscriptions := 0
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		conn.mu.Lock()
		subscriptions++
		conn.mu.Unlock()
		return func(yield func(a2a.Event, error) bool) {}
	}
	s := fastReconnect(NewSession(conn), 20)
	defer s.Shutdown()

	s.SendStreaming(context.Background(), "hi", SendOptions{})

	done, seen := drain(s, isStreamDone, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	if done.(StreamDoneEvent).Err != nil {
		t.Fatalf("done err = %v", done.(StreamDoneEvent).Err)
	}
	for _, ev := range seen {
		if _, ok := ev.(ReconnectingEvent); ok {
			t.Fatalf("input-required must not reconnect; seen=%v", seen)
		}
	}
	if subscriptions != 0 {
		t.Fatalf("subscribe calls = %d, want 0", subscriptions)
	}
}

// TestSubscribeInteractiveTaskDoesNotLoop: subscribing to a task already
// parked in auth-required yields its snapshot and ends — no reconnect loop.
func TestSubscribeInteractiveTaskDoesNotLoop(t *testing.T) {
	conn := &fakeConn{}
	conn.subscribe = func(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			snap := &a2a.Task{ID: a2a.TaskID(id), ContextID: "c-a",
				Status: a2a.TaskStatus{State: a2a.TaskStateAuthRequired}}
			yield(snap, nil)
		}
	}
	s := fastReconnect(NewSession(conn), 20)
	defer s.Shutdown()

	s.Subscribe(context.Background(), "t-auth")

	done, seen := drain(s, func(ev Event) bool {
		d, ok := ev.(StreamDoneEvent)
		return ok && d.Op == "subscribe"
	}, 2*time.Second)
	if done == nil {
		t.Fatalf("no done; seen=%v", seen)
	}
	if done.(StreamDoneEvent).Err != nil {
		t.Fatalf("done err = %v", done.(StreamDoneEvent).Err)
	}
	for _, ev := range seen {
		if _, ok := ev.(ReconnectingEvent); ok {
			t.Fatalf("auth-required must not reconnect; seen=%v", seen)
		}
	}
}

// TestSendRawMergesMessageMetadata: the message's own metadata keys survive
// requestFor; the engine capabilities still take precedence on conflicts.
func TestSendRawMergesMessageMetadata(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()

	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))
	msg.Metadata = map[string]any{
		"custom":                   "kept",
		"a2uiRendererCapabilities": "stale",
		"a2uiRendererDataModel":    map[string]any{"stale": true},
	}
	s.SendRaw(context.Background(), msg, SendOptions{Metadata: map[string]any{"extra": 1}})

	deadline := time.After(2 * time.Second)
	for conn.lastSend() == nil {
		select {
		case <-deadline:
			t.Fatal("no send captured")
		case <-time.After(time.Millisecond):
		}
	}
	meta := conn.lastSend().Message.Metadata
	if meta["custom"] != "kept" {
		t.Fatalf("custom key lost: %v", meta)
	}
	if meta["extra"] != 1 {
		t.Fatalf("extra key lost: %v", meta)
	}
	caps, ok := meta["a2uiRendererCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities not a map: %v", meta["a2uiRendererCapabilities"])
	}
	if _, exists := caps["v1.0"]; !exists {
		t.Fatalf("engine capabilities must win over the message's stale value: %v", caps)
	}
}

// TestSendRawAssignsMissingMessageID: a hand-built message without an ID
// gets one, so concurrent operations never share a cancel key.
func TestSendRawAssignsMissingMessageID(t *testing.T) {
	conn := &fakeConn{}
	s := NewSession(conn)
	defer s.Shutdown()

	msg := &a2a.Message{Role: a2a.MessageRoleUser, Parts: []*a2a.Part{a2a.NewTextPart("hi")}}
	s.SendRaw(context.Background(), msg, SendOptions{})

	deadline := time.After(2 * time.Second)
	for conn.lastSend() == nil {
		select {
		case <-deadline:
			t.Fatal("no send captured")
		case <-time.After(time.Millisecond):
		}
	}
	if conn.lastSend().Message.ID == "" {
		t.Fatal("empty message ID reached the wire")
	}
}

// TestShutdownClosesDone: the Done channel must close on Shutdown so the
// TUI's bridge listener (select on Done) is never left blocked forever.
func TestShutdownClosesDone(t *testing.T) {
	s := NewSession(&fakeConn{})
	s.Shutdown()
	select {
	case <-s.Done():
	default:
		// Shutdown is synchronous through cancel(); give it a grace period
		// only to absorb scheduler noise.
		select {
		case <-s.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("Done not closed after Shutdown")
		}
	}
	// Idempotent: a second Shutdown must not panic on the channel.
	s.Shutdown()
}
