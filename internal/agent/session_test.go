package agent

import (
	"context"
	"errors"
	"iter"
	"sync"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// fakeConn is an in-memory AgentConn used to drive the session pumps.
type fakeConn struct {
	mu sync.Mutex

	sends     []*a2a.SendMessageRequest
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
			yield(a2a.NewStatusUpdateEvent(info, a2a.TaskStateWorking, nil), nil)
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
