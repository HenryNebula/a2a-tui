// Package e2e exercises the full a2a-tui stack — card resolution, both
// wire-protocol clients, the session orchestration (pumps, registry, push
// delivery, A2UI engine) — against in-process agents: the deterministic
// fixture agent (A2A 1.0 over the real SDK server) and a hand-rolled 0.3
// server pinned to the legacy wire format. The TUI is deliberately not
// involved: these tests drive agent.Session's event channel directly.
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/fixtureagent"
)

// e2eTimeout is the generous per-condition polling deadline: far above any
// observed latency, still small enough to fail fast.
const e2eTimeout = 10 * time.Second

// connectFixture serves the fixture agent in-process and connects through
// the real stack: Resolve (card fetch + protocol detect) → NewConn (SDK
// client) → NewSession. The session is shut down on test cleanup.
func connectFixture(t *testing.T) *agent.Session {
	t.Helper()
	ts, err := fixtureagent.StartTest()
	if err != nil {
		t.Fatalf("fixture agent: %v", err)
	}
	t.Cleanup(ts.Close)
	return connect(t, ts.URL)
}

// connect resolves and connects to base through the real stack.
func connect(t *testing.T, base string) *agent.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()
	res, err := agent.Resolve(ctx, nil, base, agent.Auto)
	if err != nil {
		t.Fatalf("resolve %s: %v", base, err)
	}
	conn, err := agent.NewConn(ctx, res, nil)
	if err != nil {
		t.Fatalf("connect %s (%s): %v", base, res.Wire, err)
	}
	s := agent.NewSession(conn)
	t.Cleanup(s.Shutdown)
	return s
}

// collectUntil drains session events until pred matches (returning the
// matching event and everything seen) or fails at the deadline.
func collectUntil(t *testing.T, s *agent.Session, what string, pred func(agent.Event) bool) (agent.Event, []agent.Event) {
	t.Helper()
	deadline := time.After(e2eTimeout)
	var seen []agent.Event
	for {
		select {
		case ev := <-s.Events():
			seen = append(seen, ev)
			if pred(ev) {
				return ev, seen
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s; seen: %s", what, summarize(seen))
			return nil, seen
		}
	}
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(e2eTimeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// isDone reports whether ev is the StreamDoneEvent for op with no error.
func isDone(ev agent.Event, op string) bool {
	d, ok := ev.(agent.StreamDoneEvent)
	return ok && d.Op == op && d.Err == nil
}

// taskUpdates filters events to task-state updates, preserving order.
func taskUpdates(events []agent.Event) []agent.TaskUpdateEvent {
	var out []agent.TaskUpdateEvent
	for _, ev := range events {
		if u, ok := ev.(agent.TaskUpdateEvent); ok {
			out = append(out, u)
		}
	}
	return out
}

// textsOf joins the text of an agent message.
func textsOf(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var out string
	for _, p := range m.Parts {
		if p != nil && p.Text() != "" {
			if out != "" {
				out += " "
			}
			out += p.Text()
		}
	}
	return out
}

// summarize renders seen events compactly for failure messages.
func summarize(events []agent.Event) string {
	out := ""
	for i, ev := range events {
		if i > 0 {
			out += "; "
		}
		switch e := ev.(type) {
		case agent.TaskUpdateEvent:
			out += "task:" + string(e.State) + "/" + e.Source
		case agent.AgentMessageEvent:
			out += "msg:" + textsOf(e.Msg)
		case agent.ArtifactEvent:
			out += "artifact:" + string(e.Artifact.ID) + "/append=" + boolStr(e.Append)
		case agent.StreamDoneEvent:
			out += "done:" + e.Op
		case agent.ErrorEvent:
			out += "err:" + e.Op + ":" + e.Err.Error()
		default:
			out += "other"
		}
	}
	return out
}

func boolStr(b bool) string {
	if b {
		return "t"
	}
	return "f"
}
