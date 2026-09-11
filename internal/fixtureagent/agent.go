// Package fixtureagent implements a deterministic, locally-runnable A2A
// v1.0 agent ("a2a-tui fixture agent") used to manually exercise a2a-tui and
// to drive automated e2e tests. Behavior is scripted by the FIRST keyword of
// each user message (case-insensitive); anything unmatched is echoed back as
// a completed task. See KeywordHelp for the full table.
//
// The agent is served by the a2a-go SDK server stack (see server.go): the
// executor only emits a2a.Event values through the SDK helpers
// (NewSubmittedTask, NewStatusUpdateEvent, NewArtifactEvent, ...) and the
// SDK's processor persists tasks, fans out SSE frames and delivers push
// notifications.
package fixtureagent

import (
	"context"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// Tunables for the scripted behaviors. All generous enough to keep tests
// flake-free, all deterministic in output.
const (
	// streamChunkLimit bounds the "stream <n>" chunk count.
	streamChunkLimit = 64
	// maxDelay bounds "slow"/"cancelme" sleeps.
	maxDelay = time.Hour
	// pushStepCount/pushStepDelay pace the "push" keyword so a client has
	// time to register a webhook while the task is still emitting events.
	pushStepCount = 6
	pushStepDelay = 400 * time.Millisecond
)

// KeywordHelp is the fixture's keyword cheat sheet, one row per behavior.
// HelpText and the "skills" reply are rendered from it.
var KeywordHelp = []struct {
	Keyword string
	Help    string
}{
	{"stream <n>", "working status, then n streamed chunks, then completed"},
	{"artifact", "completed with a markdown text artifact"},
	{"artifact append", "artifact grown through append (lastChunk) events"},
	{"inputreq", "input-required question; answering on the same task completes it"},
	{"push", "slow status updates; register a push webhook to receive them"},
	{"slow <seconds>", "working, a cancelable sleep, completed"},
	{"fail", "failed task"},
	{"cancelme <seconds>", "long working phase; CancelTask moves it to canceled"},
	{"a2ui form", "contact-form surface exercising every input component"},
	{"a2ui dynamic", "formatString-bound text plus a live-update button"},
	{"a2ui list", "list template over a five-element data array"},
	{"skills", "this keyword list"},
	{"<anything else>", "echo: <text>"},
}

// HelpText renders KeywordHelp as an indented two-column list.
func HelpText() string {
	var sb strings.Builder
	sb.WriteString("keywords (first word of the message, case-insensitive):\n")
	for _, row := range KeywordHelp {
		fmt.Fprintf(&sb, "  %-20s %s\n", row.Keyword, row.Help)
	}
	return sb.String()
}

// skillsText is the "skills" reply body.
func skillsText() string {
	var sb strings.Builder
	sb.WriteString("a2a-tui fixture agent — scripted behaviors:\n")
	for _, row := range KeywordHelp {
		fmt.Fprintf(&sb, "  %-20s %s\n", row.Keyword, row.Help)
	}
	return sb.String()
}

// Agent is an a2asrv.AgentExecutor whose behavior is scripted by the first
// keyword of the user message. It is safe for concurrent use.
type Agent struct {
	// signals carries an in-band cancel signal per task. The SDK runs
	// executions in a detached context, so CancelTask does NOT cancel the
	// executor's ctx; the fixture closes the task's channel instead so
	// long-running behaviors ("cancelme", "slow", "push") stop promptly.
	mu      sync.Mutex
	signals map[a2a.TaskID]chan struct{}
}

// NewAgent returns a fresh fixture executor.
func NewAgent() *Agent {
	return &Agent{signals: make(map[a2a.TaskID]chan struct{})}
}

// Compile-time interface check.
var _ a2asrv.AgentExecutor = (*Agent)(nil)

// signal returns the task's cancel channel, creating it on first use.
func (a *Agent) signal(tid a2a.TaskID) <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	ch, ok := a.signals[tid]
	if !ok {
		ch = make(chan struct{})
		a.signals[tid] = ch
	}
	return ch
}

// cancel closes and forgets the task's cancel channel, if any.
func (a *Agent) cancel(tid a2a.TaskID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ch, ok := a.signals[tid]; ok {
		close(ch)
		delete(a.signals, tid)
	}
}

// forget drops the task's cancel channel without closing it (execution
// ended; nobody is listening anymore).
func (a *Agent) forget(tid a2a.TaskID) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.signals, tid)
}

// Execute implements a2asrv.AgentExecutor. Dispatch precedence:
//
//  1. a renderer A2UI action envelope in the message (see a2ui.go) —
//     the fixture answers with surface updates as a message;
//  2. the message's first keyword "a2ui" — the fixture answers with a
//     surface as a message;
//  3. a continuation of a task parked in TASK_STATE_INPUT_REQUIRED —
//     the fixture answers "you answered: <text>" (task-based);
//  4. any other keyword or plain text — task-based behavior, defaulting
//     to echo.
//
// Message replies and task replies are mutually exclusive in the SDK: a
// Message event is rejected once the execution has stored a Task, so the
// a2ui flows are context-scoped (no task is created; clients continue via
// contextId) while every other keyword runs as a task.
func (a *Agent) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		x := &execution{agent: a, ctx: ctx, ec: ec, yield: yield}
		defer a.forget(ec.TaskID)

		if turn := findAction(ec.Message); turn != nil {
			a.runAction(x, turn)
			return
		}

		keyword, args := parseScript(messageText(ec.Message))
		if keyword == "a2ui" {
			a.runA2UI(x, args)
			return
		}

		if ec.StoredTask != nil && ec.StoredTask.Status.State == a2a.TaskStateInputRequired {
			x.status(a2a.TaskStateCompleted, "you answered: "+messageText(ec.Message))
			return
		}

		if ec.StoredTask == nil {
			if !x.emit(a2a.NewSubmittedTask(ec, ec.Message)) {
				return
			}
		}
		switch keyword {
		case "stream":
			a.runStream(x, args)
		case "artifact":
			if len(args) > 0 && strings.EqualFold(args[0], "append") {
				a.runArtifactAppend(x)
			} else {
				a.runArtifact(x)
			}
		case "inputreq":
			a.runInputRequired(x)
		case "push":
			a.runPush(x)
		case "slow":
			a.runSlow(x, args)
		case "fail":
			a.runFail(x)
		case "cancelme":
			a.runCancelMe(x, args)
		case "skills":
			x.status(a2a.TaskStateCompleted, skillsText())
		default:
			x.status(a2a.TaskStateCompleted, "echo: "+messageText(ec.Message))
		}
	}
}

// Cancel implements a2asrv.AgentExecutor: mark the task canceled and stop
// any in-flight execution via the cancel signal.
func (a *Agent) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		a.cancel(ec.TaskID)
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCanceled,
			a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart("canceled by client"))), nil)
	}
}

// execution bundles one Execute call's plumbing.
type execution struct {
	agent *Agent
	ctx   context.Context
	ec    *a2asrv.ExecutorContext
	yield func(a2a.Event, error) bool
}

// emit yields one event, reporting whether to keep going.
func (x *execution) emit(ev a2a.Event) bool { return x.yield(ev, nil) }

// status emits a status update whose optional status message carries text.
func (x *execution) status(state a2a.TaskState, text string) bool {
	var msg *a2a.Message
	if text != "" {
		msg = a2a.NewMessageForTask(a2a.MessageRoleAgent, x.ec, a2a.NewTextPart(text))
	}
	return x.emit(a2a.NewStatusUpdateEvent(x.ec, state, msg))
}

// statusWithPart emits a completing status update whose message carries
// arbitrary parts (used when an a2ui batch must ride on a task turn).
func (x *execution) statusWithPart(state a2a.TaskState, part *a2a.Part) bool {
	msg := a2a.NewMessageForTask(a2a.MessageRoleAgent, x.ec, part)
	return x.emit(a2a.NewStatusUpdateEvent(x.ec, state, msg))
}

// contextMessage emits a terminal agent message bound to the execution's
// context only: taskless exchanges create no task, so clients continue the
// conversation via contextId.
func (x *execution) contextMessage(parts ...*a2a.Part) bool {
	msg := a2a.NewMessage(a2a.MessageRoleAgent, parts...)
	msg.ContextID = x.ec.ContextID
	return x.emit(msg)
}

// sleep waits for d, returning false when the task was canceled (via
// CancelTask) or the execution context died; true when the delay elapsed.
func (x *execution) sleep(d time.Duration) bool {
	if d <= 0 {
		return x.ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-x.agent.signal(x.ec.TaskID):
		return false
	case <-x.ctx.Done():
		return false
	}
}

// runStream emits a working status, n chunk-carrying working statuses and a
// completing status. Over SSE each chunk arrives as its own data frame.
func (a *Agent) runStream(x *execution, args []string) {
	n := parseCount(args, 3, 1, streamChunkLimit)
	if !x.status(a2a.TaskStateWorking, fmt.Sprintf("streaming %d chunks", n)) {
		return
	}
	for i := 1; i <= n; i++ {
		if !x.status(a2a.TaskStateWorking, fmt.Sprintf("chunk %d/%d", i, n)) {
			return
		}
	}
	x.status(a2a.TaskStateCompleted, fmt.Sprintf("stream complete: %d chunks", n))
}

// runArtifact completes the task with a single markdown text artifact.
func (a *Agent) runArtifact(x *execution) {
	if !x.status(a2a.TaskStateWorking, "building artifact") {
		return
	}
	ev := a2a.NewArtifactEvent(x.ec, markdownPart("# Fixture artifact\n\nA short **markdown** text part, generated deterministically."))
	ev.LastChunk = true
	if !x.emit(ev) {
		return
	}
	x.status(a2a.TaskStateCompleted, "artifact delivered")
}

// runArtifactAppend streams an artifact whose content grows through append
// events; the final update carries lastChunk.
func (a *Agent) runArtifactAppend(x *execution) {
	if !x.status(a2a.TaskStateWorking, "streaming artifact (append mode)") {
		return
	}
	first := a2a.NewArtifactEvent(x.ec, markdownPart("# Appended fixture artifact"))
	if !x.emit(first) {
		return
	}
	id := first.Artifact.ID
	chunks := []string{
		"\n\nSecond stanza, appended.",
		"\n\nThird and final stanza (lastChunk).",
	}
	for i, chunk := range chunks {
		upd := a2a.NewArtifactUpdateEvent(x.ec, id, markdownPart(chunk))
		upd.LastChunk = i == len(chunks)-1
		if !x.emit(upd) {
			return
		}
	}
	x.status(a2a.TaskStateCompleted, "artifact complete")
}

// runInputRequired parks the task in input-required with a question. The
// answer turn is handled by Execute's continuation branch.
func (a *Agent) runInputRequired(x *execution) {
	x.status(a2a.TaskStateInputRequired, "What is your favorite color? (reply on the same task)")
}

// runPush emits slow status updates so a client can register a push webhook
// mid-flight and observe the SDK delivering each event to it.
func (a *Agent) runPush(x *execution) {
	if !x.status(a2a.TaskStateWorking, "pushing status updates") {
		return
	}
	for i := 1; i <= pushStepCount; i++ {
		if !x.sleep(pushStepDelay) {
			return
		}
		if !x.status(a2a.TaskStateWorking, fmt.Sprintf("push status %d/%d", i, pushStepCount)) {
			return
		}
	}
	x.status(a2a.TaskStateCompleted, "push test complete")
}

// runSlow works, sleeps (cancel-aware), completes.
func (a *Agent) runSlow(x *execution, args []string) {
	d := parseSeconds(args, time.Second)
	if !x.status(a2a.TaskStateWorking, "sleeping "+d.String()) {
		return
	}
	if !x.sleep(d) {
		return
	}
	x.status(a2a.TaskStateCompleted, "slept "+d.String())
}

// runFail fails the task with an explanatory message.
func (a *Agent) runFail(x *execution) {
	x.status(a2a.TaskStateFailed, "deliberate failure (fixture)")
}

// runCancelMe works for the requested duration unless CancelTask lands
// first, in which case the execution stops silently (the Cancel path has
// already written the canceled state).
func (a *Agent) runCancelMe(x *execution, args []string) {
	d := parseSeconds(args, 30*time.Second)
	if !x.status(a2a.TaskStateWorking, "working for "+d.String()+" — cancel me") {
		return
	}
	if !x.sleep(d) {
		return
	}
	x.status(a2a.TaskStateCompleted, "cancelme finished without being canceled")
}

// runA2UI dispatches the a2ui sub-keywords; surfaces are defined in a2ui.go.
func (a *Agent) runA2UI(x *execution, args []string) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "form":
		a2uiReply(x, createSurfaceEnvelope(formSurface(x.ec.TaskID)))
	case "dynamic":
		a2uiReply(x, createSurfaceEnvelope(dynamicSurface(x.ec.TaskID)))
	case "list":
		a2uiReply(x, createSurfaceEnvelope(listSurface(x.ec.TaskID)))
	default:
		x.contextMessage(a2a.NewTextPart("a2ui: unknown script (try: a2ui form, a2ui dynamic, a2ui list)"))
	}
}

// runAction answers a renderer A2UI action with surface updates.
func (a *Agent) runAction(x *execution, turn *actionTurn) {
	action := turn.action
	switch {
	case action.Name == "submit":
		// Write the submitted summary into the data model, then swap the
		// pressed button for an acknowledgment text.
		name := turn.contextString("name", "anonymous")
		a2uiReply(x,
			updateDataModelEnvelope(action.SurfaceID, "/submitted", map[string]any{
				"name": name,
				"task": string(x.ec.TaskID),
			}),
			updateComponentsEnvelope(action.SurfaceID, textComponent(action.SourceComponentID, "Submitted ✓", "body")),
		)
	case strings.HasPrefix(action.SurfaceID, dynamicSurfaceIDPrefix):
		// Live-update demo: bump the values the bound text renders from.
		a2uiReply(x, updateDataModelEnvelope(action.SurfaceID, "", map[string]any{
			"count": turn.modelCount(action.SurfaceID, "count", 1) + 1,
			"label": "refreshed",
		}))
	default:
		// Generic deterministic acknowledgment: replace the source
		// component with a text naming the received action.
		a2uiReply(x, updateComponentsEnvelope(action.SurfaceID,
			textComponent(action.SourceComponentID, "received action "+action.Name, "body")))
	}
}

// parseScript splits a message into its lowercased first keyword and the
// remaining fields.
func parseScript(text string) (string, []string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", nil
	}
	return strings.ToLower(fields[0]), fields[1:]
}

// parseCount reads args[0] as an int, falling back to def and clamping to
// [min, max].
func parseCount(args []string, def, min, max int) int {
	n := def
	if len(args) > 0 {
		if v, err := strconv.Atoi(args[0]); err == nil {
			n = v
		}
	}
	if n < min {
		n = min
	}
	if n > max {
		n = max
	}
	return n
}

// parseSeconds reads args[0] as a fractional second count, falling back to
// def and clamping to [0, maxDelay].
func parseSeconds(args []string, def time.Duration) time.Duration {
	d := def
	if len(args) > 0 {
		if f, err := strconv.ParseFloat(args[0], 64); err == nil && f >= 0 {
			d = time.Duration(f * float64(time.Second))
		}
	}
	if d > maxDelay {
		d = maxDelay
	}
	return d
}

// messageText concatenates the text parts of a message (data and file parts
// are ignored), mirroring how the SDK's reference CLI extracts input.
func messageText(msg *a2a.Message) string {
	if msg == nil {
		return ""
	}
	texts := make([]string, 0, len(msg.Parts))
	for _, p := range msg.Parts {
		if t := p.Text(); t != "" {
			texts = append(texts, t)
		}
	}
	return strings.TrimSpace(strings.Join(texts, " "))
}

// markdownPart wraps text in a text/markdown part.
func markdownPart(text string) *a2a.Part {
	part := a2a.NewTextPart(text)
	part.MediaType = "text/markdown"
	return part
}
