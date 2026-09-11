package agent

import (
	"context"
	"iter"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// Event is the neutral session-event type delivered on Session.Events.
// internal/agent deliberately does not import bubbletea: the TUI wraps
// these values into tea.Msg itself (see internal/tui/bridge.go).
type Event interface{}

// Session events. Keep the set small and UI-agnostic; everything an A2A
// agent can emit maps onto one of these.
type (
	// StatusLineEvent asks the UI to show a transient status line.
	StatusLineEvent struct{ Text string }

	// TaskUpdateEvent reports a task state transition or snapshot.
	TaskUpdateEvent struct {
		TaskID     string
		ContextID  string
		State      a2a.TaskState
		StatusText string    // sanitized status message text, may be empty
		Snapshot   *a2a.Task // full task when known (results, subscribe head)
		Source     string    // "send" | "stream" | "subscribe" | "cancel" | "fetch"
	}

	// AgentMessageEvent delivers a complete agent-authored message.
	AgentMessageEvent struct{ Msg *a2a.Message }

	// ArtifactEvent delivers one artifact update (stream or result).
	ArtifactEvent struct {
		TaskID    string
		Artifact  *a2a.Artifact
		Append    bool
		LastChunk bool
	}

	// StreamCompactedEvent reports that backpressure dropped updates; the
	// view should re-fetch the task (GetTask) to resync.
	StreamCompactedEvent struct {
		TaskID  string
		Source  string
		Dropped int
	}

	// StreamDoneEvent marks the end of a send / stream / subscribe
	// operation. Err is non-nil on failure (including local cancel).
	StreamDoneEvent struct {
		Op  string // "send" | "stream" | "subscribe"
		Err error
	}

	// ErrorEvent reports an operation failure outside stream lifecycle.
	ErrorEvent struct {
		Op  string
		Err error
	}
)

// Session parameters.
const (
	// eventBufferSize is the session event channel capacity.
	eventBufferSize = 1024
	// publishTimeout bounds how long terminal events (done, errors) wait
	// for a stalled consumer before being dropped.
	publishTimeout = 2 * time.Second
	// shutdownGrace bounds Shutdown's wait for pump goroutines.
	shutdownGrace = 5 * time.Second
)

// SendOptions parametrize Send and SendStreaming.
type SendOptions struct {
	// TaskID continues an existing task (e.g. replying to an
	// input-required task).
	TaskID string
	// ContextID attaches the conversation context.
	ContextID string
	// HistoryLength requests n most recent history messages in results.
	HistoryLength *int
	// Metadata is merged over the A2UI renderer capabilities metadata.
	Metadata map[string]any
}

// Session owns one live agent connection: it builds requests, runs the
// send/stream/subscribe pumps, feeds the task registry, and publishes
// neutral Events for the UI. Session itself never touches bubbletea.
//
// The events channel is never closed; shutdown is signaled by canceling
// the session context (pumps stop, remaining events are dropped).
type Session struct {
	conn   AgentConn
	reg    *Registry
	eng    *a2ui.Engine
	events chan Event

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // op key → cancel
	tasks   map[string]string             // task ID → op key (first op that saw it)
}

// NewSession wires a session over conn. conn must be non-nil.
func NewSession(conn AgentConn) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		conn:    conn,
		reg:     NewRegistry(),
		eng:     a2ui.NewEngine(),
		events:  make(chan Event, eventBufferSize),
		ctx:     ctx,
		cancel:  cancel,
		cancels: map[string]context.CancelFunc{},
		tasks:   map[string]string{},
	}
}

// Conn exposes the underlying connection.
func (s *Session) Conn() AgentConn { return s.conn }

// Engine exposes the session's A2UI engine (surface state).
func (s *Session) Engine() *a2ui.Engine { return s.eng }

// Registry exposes the observed-task registry.
func (s *Session) Registry() *Registry { return s.reg }

// Events is the session's event stream. It is never closed; stop reading
// only after Shutdown.
func (s *Session) Events() <-chan Event { return s.events }

// Send fires a blocking SendMessage in the background and publishes the
// result as events, ending with StreamDoneEvent{Op: "send"}.
func (s *Session) Send(ctx context.Context, text string, opts SendOptions) {
	s.SendRaw(ctx, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text)), opts)
}

// SendRaw is Send with a caller-built message (e.g. an A2UI action riding
// in a data part). The message's role, parts and continuation IDs are used
// as-is; SendOptions.TaskID/ContextID override when set, and the A2UI
// renderer capabilities (+ data-model metadata) are merged over the
// message's own metadata.
func (s *Session) SendRaw(ctx context.Context, msg *a2a.Message, opts SendOptions) {
	req := s.requestFor(msg, opts)
	key := "send:" + msg.ID
	opCtx, cancel := s.register(ctx, key)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer s.release(key, cancel)

		res, err := s.conn.SendMessage(opCtx, req)
		if err != nil {
			err = mapCancel(err, opCtx)
			s.publish(ErrorEvent{Op: "send", Err: err})
			s.publishWait(StreamDoneEvent{Op: "send", Err: err})
			return
		}
		switch r := res.(type) {
		case *a2a.Message:
			if r != nil {
				s.reg.Observe(r)
				s.publish(AgentMessageEvent{Msg: r})
			}
		case *a2a.Task:
			for _, e := range s.taskEvents(r, "send") {
				s.publish(e)
			}
		}
		s.publishWait(StreamDoneEvent{Op: "send"})
	}()
}

// SendStreaming fires a SendStreamingMessage and pumps the event
// sequence into the session channel, ending with
// StreamDoneEvent{Op: "stream"}. Backpressured updates are dropped and
// reported via StreamCompactedEvent.
func (s *Session) SendStreaming(ctx context.Context, text string, opts SendOptions) {
	s.SendRawStreaming(ctx, a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(text)), opts)
}

// SendRawStreaming is SendRaw over the streaming transport.
func (s *Session) SendRawStreaming(ctx context.Context, msg *a2a.Message, opts SendOptions) {
	req := s.requestFor(msg, opts)
	key := "stream:" + msg.ID
	opCtx, cancel := s.register(ctx, key)
	seq := s.conn.SendStreamingMessage(opCtx, req)
	s.wg.Add(1)
	go s.pump(opCtx, key, cancel, "stream", "", seq)
}

// Subscribe pumps SubscribeToTask for a task. Per the spec the agent
// emits a full Task snapshot as the first event.
func (s *Session) Subscribe(ctx context.Context, taskID string) {
	key := "subscribe:" + taskID
	opCtx, cancel := s.register(ctx, key)
	s.bindTask(taskID, key)
	seq := s.conn.SubscribeToTask(opCtx, taskID)
	s.wg.Add(1)
	go s.pump(opCtx, key, cancel, "subscribe", taskID, seq)
}

// CancelActive cancels every in-flight send/stream/subscribe operation
// and reports how many were canceled. Esc in the UI maps here.
func (s *Session) CancelActive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for k, c := range s.cancels {
		c()
		delete(s.cancels, k)
		n++
	}
	return n
}

// Shutdown cancels all operations, waits (bounded) for pumps to exit,
// and destroys the connection. It is idempotent: the connection is
// destroyed exactly once.
func (s *Session) Shutdown() {
	s.once.Do(func() {
		s.cancel()
		s.mu.Lock()
		for k, c := range s.cancels {
			c()
			delete(s.cancels, k)
		}
		s.mu.Unlock()

		done := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shutdownGrace):
		}
		if s.conn != nil {
			_ = s.conn.Destroy()
		}
	})
}

// pump drains one SDK event sequence into the session channel.
func (s *Session) pump(ctx context.Context, key string, cancel context.CancelFunc, op, initialTaskID string, seq iter.Seq2[a2a.Event, error]) {
	defer s.wg.Done()
	defer s.release(key, cancel)

	taskID := initialTaskID
	dropped := 0
	for ev, err := range seq {
		if err != nil {
			err = mapCancel(err, ctx)
			s.publish(ErrorEvent{Op: op, Err: err})
			s.publishWait(StreamDoneEvent{Op: op, Err: err})
			return
		}
		if ev == nil {
			continue
		}
		if ctx.Err() != nil {
			s.publishWait(StreamDoneEvent{Op: op, Err: ctx.Err()})
			return
		}
		s.reg.Observe(ev)
		if id := eventTaskID(ev); id != "" && taskID == "" {
			taskID = id
			s.bindTask(taskID, key)
		}
		for _, e := range s.eventsFor(ev, op) {
			if !s.publish(e) {
				dropped++
			}
		}
		if terminal := eventState(ev); terminal != a2a.TaskStateUnspecified && terminal.Terminal() {
			break // stream ends at terminal state even if the server lingers
		}
	}
	if dropped > 0 {
		s.publishWait(StreamCompactedEvent{TaskID: taskID, Source: op, Dropped: dropped})
	}
	s.publishWait(StreamDoneEvent{Op: op})
}

// eventsFor translates one SDK event into session events.
func (s *Session) eventsFor(ev a2a.Event, op string) []Event {
	switch e := ev.(type) {
	case *a2a.Task:
		return s.taskEvents(e, op)
	case *a2a.TaskStatusUpdateEvent:
		out := []Event{TaskUpdateEvent{
			TaskID:     string(e.TaskID),
			ContextID:  e.ContextID,
			State:      e.Status.State,
			StatusText: messageText(e.Status.Message),
			Source:     op,
		}}
		if e.Status.Message != nil {
			out = append(out, AgentMessageEvent{Msg: e.Status.Message})
		}
		return out
	case *a2a.TaskArtifactUpdateEvent:
		return []Event{ArtifactEvent{
			TaskID:    string(e.TaskID),
			Artifact:  e.Artifact,
			Append:    e.Append,
			LastChunk: e.LastChunk,
		}}
	case *a2a.Message:
		return []Event{AgentMessageEvent{Msg: e}}
	}
	return nil
}

// taskEvents expands a task snapshot into update + artifact (+ status
// message) events.
func (s *Session) taskEvents(t *a2a.Task, op string) []Event {
	if t == nil {
		return nil
	}
	out := []Event{TaskUpdateEvent{
		TaskID:     string(t.ID),
		ContextID:  t.ContextID,
		State:      t.Status.State,
		StatusText: messageText(t.Status.Message),
		Snapshot:   t,
		Source:     op,
	}}
	for _, a := range t.Artifacts {
		if a == nil {
			continue
		}
		out = append(out, ArtifactEvent{TaskID: string(t.ID), Artifact: a, LastChunk: true})
	}
	if t.Status.Message != nil {
		out = append(out, AgentMessageEvent{Msg: t.Status.Message})
	}
	return out
}

// requestFor applies continuation IDs and config to a prebuilt message and
// merges the A2UI engine capabilities metadata (plus any sendDataModel
// surfaces and the caller's extra metadata) over the message's own.
func (s *Session) requestFor(msg *a2a.Message, opts SendOptions) *a2a.SendMessageRequest {
	if opts.TaskID != "" {
		msg.TaskID = a2a.TaskID(opts.TaskID)
	}
	if opts.ContextID != "" {
		msg.ContextID = opts.ContextID
	}
	msg.Metadata = s.outboundMetadata(opts.Metadata)

	req := &a2a.SendMessageRequest{Message: msg}
	if opts.HistoryLength != nil {
		req.Config = &a2a.SendMessageConfig{HistoryLength: opts.HistoryLength}
	}
	return req
}

// outboundMetadata merges engine capabilities and syncing data models.
func (s *Session) outboundMetadata(extra map[string]any) map[string]any {
	meta := s.eng.OutboundCapabilities()
	if dm := s.eng.DataModelMetadata(); dm != nil {
		// The attachment nests under its own key (the engine returns the
		// {version, surfaces} payload), matching the schema agents read.
		meta["a2uiRendererDataModel"] = dm
	}
	for k, v := range extra {
		meta[k] = v
	}
	return meta
}

// register derives an operation context and records its cancel func.
func (s *Session) register(parent context.Context, key string) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.cancels[key] = cancel
	s.mu.Unlock()
	return ctx, cancel
}

// release unregisters an operation's cancel func.
func (s *Session) release(key string, cancel context.CancelFunc) {
	s.mu.Lock()
	delete(s.cancels, key)
	s.mu.Unlock()
	cancel()
}

// bindTask remembers which op first observed a task (future per-task
// cancellation).
func (s *Session) bindTask(taskID, key string) {
	s.mu.Lock()
	if _, ok := s.tasks[taskID]; !ok {
		s.tasks[taskID] = key
	}
	s.mu.Unlock()
}

// publish sends e without blocking; it reports whether e was delivered.
func (s *Session) publish(e Event) bool {
	select {
	case s.events <- e:
		return true
	default:
		return false
	}
}

// publishWait sends e, waiting at most publishTimeout (or until session
// shutdown) when the consumer is stalled.
func (s *Session) publishWait(e Event) {
	timer := time.NewTimer(publishTimeout)
	defer timer.Stop()
	select {
	case s.events <- e:
	case <-timer.C:
	case <-s.ctx.Done():
	}
}

// mapCancel rewrites context-during-cancel transport noise into a clean
// context.Canceled so the UI can tell "user pressed Esc" from failure.
func mapCancel(err error, ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// eventTaskID extracts the task ID an event belongs to.
func eventTaskID(ev a2a.Event) string {
	switch e := ev.(type) {
	case *a2a.Task:
		return string(e.ID)
	case *a2a.TaskStatusUpdateEvent:
		return string(e.TaskID)
	case *a2a.TaskArtifactUpdateEvent:
		return string(e.TaskID)
	case *a2a.Message:
		return string(e.TaskID)
	}
	return ""
}

// eventState extracts the task state an event reports, if any.
func eventState(ev a2a.Event) a2a.TaskState {
	switch e := ev.(type) {
	case *a2a.Task:
		return e.Status.State
	case *a2a.TaskStatusUpdateEvent:
		return e.Status.State
	}
	return a2a.TaskStateUnspecified
}
