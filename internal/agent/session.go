package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/pushsrv"
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

	// ReconnectingEvent reports that a live stream or subscribe ended while
	// its task was still non-terminal and the session is resubscribing via
	// SubscribeToTask. Err carries the disconnect cause (nil when the
	// iterator simply ended).
	ReconnectingEvent struct {
		TaskID  string
		Attempt int
		Err     error
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

	// Default reconnect tunables for the stream pump (see Session fields
	// for the overridable copies).
	defaultReconnectInitial  = 250 * time.Millisecond
	defaultReconnectMax      = 5 * time.Second
	defaultReconnectAttempts = 20
	// reconnectJitter is the ± fraction applied to each backoff delay.
	reconnectJitter = 0.2
	// pushRegisterTimeout bounds one CreateTaskPushConfig call.
	pushRegisterTimeout = 10 * time.Second
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

	// Reconnect knobs (copies of the defaults so tests can shorten them).
	reconnectInitial  time.Duration
	reconnectMax      time.Duration
	reconnectAttempts int

	mu      sync.Mutex
	cancels map[string]context.CancelFunc // op key → cancel
	tasks   map[string]string             // task ID → op key (first op that saw it)

	// Push-notification state (guarded by mu).
	pushSrv        *pushsrv.Server // local webhook listener when started
	pushURL        string          // advertised webhook URL
	pushToken      string          // shared bearer token
	pushEnabled    bool            // register configs for newly observed tasks?
	pushRegistered map[string]bool // task ID → config registered
	pushNoted      bool            // unsupported-note already published?
}

// NewSession wires a session over conn. conn must be non-nil.
func NewSession(conn AgentConn) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	return &Session{
		conn:              conn,
		reg:               NewRegistry(),
		eng:               a2ui.NewEngine(),
		events:            make(chan Event, eventBufferSize),
		ctx:               ctx,
		cancel:            cancel,
		cancels:           map[string]context.CancelFunc{},
		tasks:             map[string]string{},
		reconnectInitial:  defaultReconnectInitial,
		reconnectMax:      defaultReconnectMax,
		reconnectAttempts: defaultReconnectAttempts,
		pushRegistered:    map[string]bool{},
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
			s.reg.Observe(r)
			s.taskObserved(string(r.ID), key)
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

// ---------------------------------------------------------------------------
// Push notifications
// ---------------------------------------------------------------------------

// StartPush boots the local push-notification webhook (pushsrv) and
// returns the advertised URL. publicURL overrides the advertised address
// for agents behind a tunnel ("" → http://127.0.0.1:<port>/push). Starting
// does not yet register anything: call SetPushEnabled(true) to register a
// TaskPushNotificationConfig for every task observed from then on. Remote
// agents cannot reach 127.0.0.1 — document a tunnel via publicURL.
func (s *Session) StartPush(publicURL string) (string, error) {
	s.mu.Lock()
	if s.pushSrv != nil {
		url := s.pushURL
		s.mu.Unlock()
		return url, nil
	}
	s.mu.Unlock()

	token, err := pushsrv.NewToken()
	if err != nil {
		return "", fmt.Errorf("push token: %w", err)
	}
	srv, err := pushsrv.Start("", token, publicURL, s.deliverPush)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	// Another StartPush may have won the race; keep the first server.
	if s.pushSrv != nil {
		url := s.pushURL
		s.mu.Unlock()
		srv.Shutdown()
		return url, nil
	}
	s.pushSrv = srv
	s.pushURL = srv.URL()
	s.pushToken = token
	s.mu.Unlock()
	return s.pushURL, nil
}

// SetPushEnabled turns automatic push-config registration for newly
// observed tasks on or off. The webhook server (StartPush) keeps running
// so already-registered tasks still deliver.
func (s *Session) SetPushEnabled(on bool) {
	s.mu.Lock()
	s.pushEnabled = on
	s.mu.Unlock()
}

// PushEnabled reports whether new tasks get push configs registered.
func (s *Session) PushEnabled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushEnabled
}

// PushURL returns the advertised webhook URL ("" while push is off).
func (s *Session) PushURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pushURL
}

// StopPush disables registration and shuts down the webhook server. It is
// idempotent.
func (s *Session) StopPush() {
	s.mu.Lock()
	s.pushEnabled = false
	srv := s.pushSrv
	s.pushSrv = nil
	s.pushURL = ""
	s.pushToken = ""
	s.mu.Unlock()
	if srv != nil {
		srv.Shutdown()
	}
}

// taskObserved records the first sight of a task: it binds the task to
// the observing operation and, when push notifications are enabled,
// registers a TaskPushNotificationConfig for it in the background.
func (s *Session) taskObserved(taskID, key string) {
	s.bindTask(taskID, key)
	s.mu.Lock()
	register := s.pushEnabled && s.pushSrv != nil && !s.pushRegistered[taskID]
	if register {
		s.pushRegistered[taskID] = true
	}
	s.mu.Unlock()
	if register {
		s.registerPushConfig(taskID)
	}
}

// registerPushConfig registers the session webhook for one task. A -32003
// PushNotificationNotSupported answer disables push (with a single
// friendly note); other failures surface as error events.
func (s *Session) registerPushConfig(taskID string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx, cancel := context.WithTimeout(s.ctx, pushRegisterTimeout)
		defer cancel()

		s.mu.Lock()
		url, token := s.pushURL, s.pushToken
		s.mu.Unlock()
		cfg := &a2a.PushConfig{
			TaskID: a2a.TaskID(taskID),
			URL:    url,
			Token:  token,
			// The shared bearer token travels as PushConfig.Token; agents
			// echo it either as A2A-Notification-Token or Authorization.
		}
		if _, err := s.conn.CreateTaskPushConfig(ctx, cfg); err != nil {
			switch {
			case errors.Is(err, a2a.ErrPushNotificationNotSupported):
				s.disablePush(err)
			case ctx.Err() != nil:
				// Session shutdown or timeout: not worth a transcript error.
			default:
				s.publish(ErrorEvent{Op: "push", Err: fmt.Errorf("register task %s: %w", taskID, err)})
			}
			return
		}
		s.publish(StatusLineEvent{Text: "⇄ push webhook registered for task " + taskID})
	}()
}

// disablePush turns push registration off after the agent rejected it,
// reporting the reason exactly once.
func (s *Session) disablePush(reason error) {
	s.mu.Lock()
	first := !s.pushNoted
	s.pushNoted = true
	s.pushEnabled = false
	s.mu.Unlock()
	if first {
		s.publishWait(StatusLineEvent{Text: "push notifications not supported by this agent — /push off (" + FriendlyError(reason) + ")"})
	}
}

// deliverPush is the pushsrv callback: it feeds the registry and
// republishes the event through the normal session pipeline with
// Source:"push" so the UI can badge push-delivered updates.
func (s *Session) deliverPush(ev a2a.Event) {
	if ev == nil || s.ctx.Err() != nil {
		return
	}
	s.reg.Observe(ev)
	if line := pushStatusLine(ev); line != "" {
		s.publish(StatusLineEvent{Text: line})
	}
	for _, e := range s.eventsFor(ev, "push") {
		s.publish(e)
	}
}

// pushStatusLine describes one push-delivered event for the transcript
// status line ("⇄ push · …").
func pushStatusLine(ev a2a.Event) string {
	switch e := ev.(type) {
	case *a2a.Task:
		if e == nil {
			return ""
		}
		return "⇄ push · task " + string(e.ID) + " " + stateLabel(e.Status.State)
	case *a2a.TaskStatusUpdateEvent:
		if e == nil {
			return ""
		}
		return "⇄ push · task " + string(e.TaskID) + " " + stateLabel(e.Status.State)
	case *a2a.TaskArtifactUpdateEvent:
		if e == nil {
			return ""
		}
		return "⇄ push · task " + string(e.TaskID) + " artifact " + string(e.Artifact.ID)
	case *a2a.Message:
		return "⇄ push · message"
	}
	return ""
}

// stateLabel renders a task state as its short lowercase name.
func stateLabel(s a2a.TaskState) string {
	name := "unspecified"
	if s != a2a.TaskStateUnspecified {
		name = string(s)
	}
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, "TASK_STATE_"), "_", "-"))
}

// Shutdown cancels all operations, waits (bounded) for pumps to exit,
// and destroys the connection. It is idempotent: the connection is
// destroyed exactly once.
func (s *Session) Shutdown() {
	s.once.Do(func() {
		s.StopPush()
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

// pump drains one SDK event sequence into the session channel. When the
// sequence ends (cleanly or on error) while the task's last known state is
// non-terminal, the pump resubscribes via SubscribeToTask with exponential
// backoff, so dropped connections heal transparently. Canceling the op
// context (Esc / CancelActive) stops the retries.
func (s *Session) pump(ctx context.Context, key string, cancel context.CancelFunc, op, initialTaskID string, seq iter.Seq2[a2a.Event, error]) {
	defer s.wg.Done()
	defer s.release(key, cancel)

	taskID := initialTaskID
	dropped := 0
	lastState := a2a.TaskStateUnspecified
	snapshot := false // next event is a resubscribe head (full task)
	attempt := 0

	for {
		var iterErr error
		terminal := false
		for ev, err := range seq {
			if err != nil {
				iterErr = mapCancel(err, ctx)
				break
			}
			if ev == nil {
				continue
			}
			if ctx.Err() != nil {
				s.finish(op, taskID, dropped, ctx.Err())
				return
			}
			s.reg.Observe(ev)
			if id := eventTaskID(ev); id != "" && taskID == "" {
				taskID = id
				s.taskObserved(taskID, key)
			}
			events := s.eventsFor(ev, op)
			if snapshot {
				events = dedupeSnapshot(events, lastState)
				snapshot = false
			}
			for _, e := range events {
				if !s.publish(e) {
					dropped++
				}
			}
			if st := eventState(ev); st != a2a.TaskStateUnspecified {
				lastState = st
			}
			if lastState.Terminal() {
				terminal = true
				break // stream ends at terminal state even if the server lingers
			}
		}

		if ctx.Err() != nil {
			s.finish(op, taskID, dropped, ctx.Err())
			return
		}
		if terminal {
			break
		}
		// The iterator ended without a terminal state. Without a task ID
		// there is nothing to resubscribe to.
		if taskID == "" {
			if iterErr != nil {
				s.publish(ErrorEvent{Op: op, Err: iterErr})
			}
			s.finish(op, taskID, dropped, iterErr)
			return
		}
		if fatalResubscribeErr(iterErr) {
			// Retrying cannot help (task gone, method unsupported): report
			// the failure and end as the pre-reconnect pump did.
			s.publish(ErrorEvent{Op: op, Err: iterErr})
			s.finish(op, taskID, dropped, iterErr)
			return
		}

		attempt++
		if attempt > s.reconnectAttempts {
			err := fmt.Errorf("gave up reconnecting task %s after %d attempts: %v",
				taskID, s.reconnectAttempts, iterErr)
			s.publish(ErrorEvent{Op: op, Err: err})
			s.finish(op, taskID, dropped, err)
			return
		}
		s.publish(ReconnectingEvent{TaskID: taskID, Attempt: attempt, Err: iterErr})
		if !s.backoffSleep(ctx, attempt) {
			s.finish(op, taskID, dropped, ctx.Err())
			return
		}
		if ctx.Err() != nil {
			s.finish(op, taskID, dropped, ctx.Err())
			return
		}
		seq = s.conn.SubscribeToTask(ctx, taskID)
		snapshot = true
	}

	s.finish(op, taskID, dropped, nil)
}

// finish publishes the compaction report (when updates were dropped) and
// the terminal done event for one pump run.
func (s *Session) finish(op, taskID string, dropped int, err error) {
	if dropped > 0 {
		s.publishWait(StreamCompactedEvent{TaskID: taskID, Source: op, Dropped: dropped})
	}
	s.publishWait(StreamDoneEvent{Op: op, Err: err})
}

// backoffSleep waits out the reconnect delay for attempt n (1-based):
// reconnectInitial doubling per attempt, capped at reconnectMax, with ±20%
// jitter. It reports false when ctx was canceled while waiting.
func (s *Session) backoffSleep(ctx context.Context, attempt int) bool {
	d := s.reconnectInitial
	for range attempt - 1 {
		d *= 2
		if d >= s.reconnectMax {
			d = s.reconnectMax
			break
		}
	}
	d += time.Duration(float64(d) * (rand.Float64()*2*reconnectJitter - reconnectJitter))
	if d < 0 {
		d = 0
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// fatalResubscribeErr reports whether err makes resubscribing pointless.
func fatalResubscribeErr(err error) bool {
	return err != nil && (errors.Is(err, a2a.ErrTaskNotFound) ||
		errors.Is(err, a2a.ErrUnsupportedOperation) ||
		errors.Is(err, a2a.ErrVersionNotSupported))
}

// dedupeSnapshot drops the leading task-update pill of a resubscribe
// snapshot when it repeats the last state already published for the task:
// per the spec the first event after SubscribeToTask is a full Task
// snapshot, and without this the transcript pill would re-render (and the
// UI re-announce) a state it is already showing. Artifacts and status
// messages are kept: they replace by ID or carry genuinely new content.
func dedupeSnapshot(events []Event, lastState a2a.TaskState) []Event {
	if len(events) == 0 {
		return events
	}
	u, ok := events[0].(TaskUpdateEvent)
	if !ok || u.State != lastState {
		return events
	}
	return events[1:]
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
