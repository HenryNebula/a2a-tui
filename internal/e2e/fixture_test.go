package e2e

// End-to-end scenarios against the in-process fixture agent (A2A 1.0 over
// the official SDK server), each driving the session event channel the way
// the TUI bridge does.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
)

func TestBlockingSendEcho(t *testing.T) {
	s := connectFixture(t)

	s.Send(context.Background(), "hello fixture", agent.SendOptions{})

	done, seen := collectUntil(t, s, "blocking send to finish", func(ev agent.Event) bool {
		return isDone(ev, "send")
	})
	if done == nil {
		return
	}
	updates := taskUpdates(seen)
	if len(updates) == 0 {
		t.Fatalf("no task updates in a blocking send: %s", summarize(seen))
	}
	last := updates[len(updates)-1]
	if last.State != a2a.TaskStateCompleted {
		t.Fatalf("final state = %s, want completed (%s)", last.State, summarize(seen))
	}
	if !strings.Contains(last.StatusText, "echo: hello fixture") {
		t.Fatalf("status text = %q, want the echo reply", last.StatusText)
	}
	// The transcript-visible form of the same reply: the task snapshot's
	// status message splits into agent text blocks.
	if last.Snapshot == nil {
		t.Fatal("blocking send carried no task snapshot")
	}
	blocks := chat.BlocksForTask(last.Snapshot, s.Engine())
	found := false
	for _, b := range blocks {
		if strings.Contains(b.Render(80), "echo: hello fixture") {
			found = true
		}
	}
	if !found {
		t.Fatalf("echo text not transcript-visible: %+v", blocks)
	}
}

func TestStreamingSendChunks(t *testing.T) {
	s := connectFixture(t)

	s.SendStreaming(context.Background(), "stream 5", agent.SendOptions{})
	_, seen := collectUntil(t, s, "stream to complete", func(ev agent.Event) bool {
		return isDone(ev, "stream")
	})

	// Ordered working statuses: "streaming 5 chunks", chunks 1..5, then
	// the completing update.
	var texts []string
	states := map[a2a.TaskState]int{}
	for _, u := range taskUpdates(seen) {
		states[u.State]++
		if u.StatusText != "" {
			texts = append(texts, u.StatusText)
		}
	}
	if states[a2a.TaskStateCompleted] == 0 {
		t.Fatalf("stream never completed: %s", summarize(seen))
	}
	if states[a2a.TaskStateWorking] < 6 { // banner + 5 chunks
		t.Fatalf("working updates = %d, want ≥6: %v", states[a2a.TaskStateWorking], texts)
	}
	want := []string{"chunk 1/5", "chunk 2/5", "chunk 3/5", "chunk 4/5", "chunk 5/5"}
	joined := strings.Join(texts, "|")
	last := -1
	for _, w := range want {
		i := strings.Index(joined, w)
		if i < 0 {
			t.Fatalf("chunk %q missing from %v", w, texts)
		}
		if i < last {
			t.Fatalf("chunks out of order: %v", texts)
		}
		last = i
	}
}

func TestArtifactAppend(t *testing.T) {
	s := connectFixture(t)

	s.SendStreaming(context.Background(), "artifact append", agent.SendOptions{})
	match, seen := collectUntil(t, s, "artifact stream to complete", func(ev agent.Event) bool {
		return isDone(ev, "stream")
	})
	_ = match

	var artifacts []agent.ArtifactEvent
	for _, ev := range seen {
		if a, ok := ev.(agent.ArtifactEvent); ok {
			artifacts = append(artifacts, a)
		}
	}
	if len(artifacts) != 3 {
		t.Fatalf("artifact events = %d, want 3 (create + 2 appends): %s", len(artifacts), summarize(seen))
	}
	if artifacts[0].Append {
		t.Error("first artifact event must not be an append")
	}
	if !artifacts[1].Append || artifacts[1].LastChunk {
		t.Errorf("second event append=%v lastChunk=%v, want true/false", artifacts[1].Append, artifacts[1].LastChunk)
	}
	if !artifacts[2].Append || !artifacts[2].LastChunk {
		t.Errorf("final event append=%v lastChunk=%v, want true/true", artifacts[2].Append, artifacts[2].LastChunk)
	}

	// The accumulated content is readable through GetTask.
	id := artifacts[0].TaskID
	task, err := s.Conn().GetTask(context.Background(), id, nil)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(task.Artifacts) == 0 {
		t.Fatal("task carries no artifacts")
	}
	var acc string
	for _, p := range task.Artifacts[0].Parts {
		acc += p.Text()
	}
	for _, stanza := range []string{"# Appended fixture artifact", "Second stanza", "Third and final stanza"} {
		if !strings.Contains(acc, stanza) {
			t.Errorf("accumulated artifact missing %q: %q", stanza, acc)
		}
	}
}

func TestInputRequiredContinuation(t *testing.T) {
	s := connectFixture(t)

	s.Send(context.Background(), "inputreq", agent.SendOptions{})
	match, seen := collectUntil(t, s, "input-required", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.State == a2a.TaskStateInputRequired
	})
	if match == nil {
		return
	}
	parked := match.(agent.TaskUpdateEvent)
	if len(taskUpdates(seen)) == 0 {
		t.Fatal("no task update seen")
	}

	// The continuation must land on the same task and complete it.
	s.Send(context.Background(), "blue", agent.SendOptions{
		TaskID:    parked.TaskID,
		ContextID: parked.ContextID,
	})
	match, seen = collectUntil(t, s, "answered task to complete", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.State == a2a.TaskStateCompleted && u.TaskID == parked.TaskID
	})
	if match == nil {
		return
	}
	if got := match.(agent.TaskUpdateEvent).StatusText; !strings.Contains(got, "you answered: blue") {
		t.Fatalf("answer = %q, want 'you answered: blue' (seen %s)", got, summarize(seen))
	}
}

func TestCancelTask(t *testing.T) {
	s := connectFixture(t)

	s.SendStreaming(context.Background(), "cancelme 30", agent.SendOptions{})
	match, _ := collectUntil(t, s, "cancelme to start working", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.State == a2a.TaskStateWorking
	})
	if match == nil {
		return
	}
	id := match.(agent.TaskUpdateEvent).TaskID

	// CancelTask is a plain one-shot RPC on the connection.
	task, err := s.Conn().CancelTask(context.Background(), id)
	if err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if task.Status.State != a2a.TaskStateCanceled {
		t.Fatalf("cancel result state = %s", task.Status.State)
	}

	// The live stream observes the canceled state and ends.
	_, seen := collectUntil(t, s, "canceled update on the stream", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.TaskID == id && u.State == a2a.TaskStateCanceled
	})
	canceledSeen := false
	for _, u := range taskUpdates(seen) {
		if u.TaskID == id && u.State == a2a.TaskStateCanceled {
			canceledSeen = true
		}
	}
	if !canceledSeen {
		t.Fatalf("no canceled pill derived from %s", summarize(seen))
	}

	// And GetTask agrees.
	waitFor(t, "GetTask to report canceled", func() bool {
		task, err := s.Conn().GetTask(context.Background(), id, nil)
		return err == nil && task.Status.State == a2a.TaskStateCanceled
	})
}

func TestSubscribeMidTask(t *testing.T) {
	s := connectFixture(t)

	// A streaming send parks the task in working for 3 seconds (a blocking
	// send would stay invisible until its result returns).
	s.SendStreaming(context.Background(), "slow 3", agent.SendOptions{})

	// Wait until the stream has carried the working state, then subscribe
	// mid-flight.
	waitFor(t, "slow task to be observed", func() bool {
		list := s.Registry().List()
		return len(list) > 0 && list[0].State == a2a.TaskStateWorking
	})
	id := s.Registry().List()[0].ID

	s.Subscribe(context.Background(), id)

	// Per the spec the first subscribe event is a full Task snapshot.
	match, seen := collectUntil(t, s, "subscribe snapshot", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.Source == "subscribe"
	})
	if match == nil {
		return
	}
	snap := match.(agent.TaskUpdateEvent)
	if snap.Snapshot == nil {
		t.Fatalf("first subscribe event carried no Task snapshot: %s", summarize(seen))
	}
	if string(snap.Snapshot.ID) != id {
		t.Fatalf("snapshot task = %s, want %s", snap.Snapshot.ID, id)
	}

	// The subscription then follows the task to completion.
	collectUntil(t, s, "subscribed task to complete", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.TaskID == id && u.State == a2a.TaskStateCompleted
	})
}

func TestPushDelivery(t *testing.T) {
	s := connectFixture(t)

	url, err := s.StartPush("")
	if err != nil {
		t.Fatalf("StartPush: %v", err)
	}
	t.Cleanup(s.StopPush)
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("push url = %q", url)
	}
	s.SetPushEnabled(true)

	s.SendStreaming(context.Background(), "push", agent.SendOptions{})

	// The fixture's slow push cadence gives the webhook time to register;
	// every delivered update arrives badged with Source "push". Waiting for
	// the terminal push also lets every earlier delivery land before the
	// webhook shuts down at cleanup.
	match, seen := collectUntil(t, s, "a push-delivered event", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.Source == "push"
	})
	if match == nil {
		return
	}
	if len(taskUpdates(seen)) == 0 {
		t.Fatal("no task updates observed")
	}
	collectUntil(t, s, "push-delivered completion", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.Source == "push" && u.State == a2a.TaskStateCompleted
	})
	// Registry tracks the push-fed state too.
	found := false
	for _, m := range s.Registry().List() {
		if m.State == a2a.TaskStateWorking || m.State == a2a.TaskStateCompleted {
			found = true
		}
	}
	if !found {
		t.Fatal("registry did not absorb push-delivered state")
	}
}

func TestA2UISurfaceRoundTrip(t *testing.T) {
	s := connectFixture(t)

	s.SendStreaming(context.Background(), "a2ui form", agent.SendOptions{})

	// The surface rides in a taskless agent message; splitting it through
	// the chat path applies the envelopes to the session engine.
	match, _ := collectUntil(t, s, "a2ui reply message", func(ev agent.Event) bool {
		_, ok := ev.(agent.AgentMessageEvent)
		return ok
	})
	if match == nil {
		return
	}
	msg := match.(agent.AgentMessageEvent).Msg
	blocks := chat.SplitMessage(msg, s.Engine())
	if len(blocks) == 0 {
		t.Fatal("a2ui message produced no blocks")
	}

	ids := s.Engine().SurfaceIDs()
	if len(ids) == 0 {
		t.Fatal("no surface created by the a2ui form round trip")
	}
	surf := s.Engine().Surface(ids[0])
	if surf == nil {
		t.Fatalf("surface %q missing", ids[0])
	}
	// A snapshot block rendered for the transcript references the surface.
	snapshot := false
	for _, b := range blocks {
		if strings.Contains(b.Render(80), ids[0]) {
			snapshot = true
		}
	}
	if !snapshot {
		t.Fatalf("no surface snapshot block for %q", ids[0])
	}
}

func TestQuiescenceAfterShutdown(t *testing.T) {
	s := connectFixture(t)

	s.SendStreaming(context.Background(), "stream 3", agent.SendOptions{})
	collectUntil(t, s, "stream to finish", func(ev agent.Event) bool {
		return isDone(ev, "stream")
	})

	// Shutdown waits (bounded) for every pump; afterwards the event
	// channel must fall silent.
	start := time.Now()
	s.Shutdown()
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("Shutdown took %v; pumps did not exit promptly", elapsed)
	}

	select {
	case ev := <-s.Events():
		t.Fatalf("event after Shutdown: %#v", ev)
	case <-time.After(500 * time.Millisecond):
	}
	// Idempotent.
	s.Shutdown()
}

// TestStreamingInputRequiredEndsCleanly: when the agent parks a task in
// input-required, the SSE stream closes by design — the session must end
// the pump (StreamDone, no error) instead of grinding the 20-attempt
// resubscribe loop against an agent that is waiting for the client.
func TestStreamingInputRequiredEndsCleanly(t *testing.T) {
	s := connectFixture(t)

	s.SendStreaming(context.Background(), "inputreq", agent.SendOptions{})
	match, _ := collectUntil(t, s, "input-required pill", func(ev agent.Event) bool {
		u, ok := ev.(agent.TaskUpdateEvent)
		return ok && u.State == a2a.TaskStateInputRequired
	})
	if match == nil {
		return
	}
	taskID := match.(agent.TaskUpdateEvent).TaskID

	done, seen := collectUntil(t, s, "stream done after input-required", func(ev agent.Event) bool {
		return isDone(ev, "stream")
	})
	if done == nil {
		return
	}
	if d := done.(agent.StreamDoneEvent); d.Err != nil {
		t.Fatalf("stream done with error: %v", d.Err)
	}
	for _, ev := range seen {
		if _, ok := ev.(agent.ReconnectingEvent); ok {
			t.Fatalf("input-required must not trigger resubscribe; seen %s", summarize(seen))
		}
	}

	// Quiescence: with the old bug the first reconnect attempt (plus its
	// eventual "gave up reconnecting" error) arrived shortly after done.
	quiet := time.NewTimer(1500 * time.Millisecond)
	defer quiet.Stop()
	for {
		select {
		case ev := <-s.Events():
			if r, ok := ev.(agent.ReconnectingEvent); ok {
				t.Fatalf("late resubscribe for task %s: %+v", taskID, r)
			}
			if e, ok := ev.(agent.ErrorEvent); ok {
				t.Fatalf("late error after input-required: %v", e.Err)
			}
		case <-quiet.C:
			return // clean: nothing further was published
		}
	}
}
