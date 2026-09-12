package e2e

// End-to-end coverage for the fixture agent's second scenario batch:
// markdown (rich rendering), wide (overflow clipping), failcode (protocol
// errors), hist (task history), each driven through the real session
// stack like the TUI bridge would.

import (
	"context"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
)

func sendBlocking(t *testing.T, s *agent.Session, text string) (agent.Event, []agent.Event) {
	t.Helper()
	s.Send(context.Background(), text, agent.SendOptions{})
	return collectUntil(t, s, "blocking send to finish", func(ev agent.Event) bool {
		return isDone(ev, "send")
	})
}

func TestMarkdownScenario(t *testing.T) {
	s := connectFixture(t)
	_, seen := sendBlocking(t, s, "markdown")
	updates := taskUpdates(seen)
	if len(updates) == 0 {
		t.Fatalf("no task updates: %s", summarize(seen))
	}
	last := updates[len(updates)-1]
	if last.State != a2a.TaskStateCompleted {
		t.Fatalf("final state = %s", last.State)
	}
	blocks := chat.BlocksForTask(last.Snapshot, s.Engine())
	var agentText string
	for _, b := range blocks {
		if ab, ok := b.(*chat.AgentTextBlock); ok {
			agentText = ab.Render(100)
		}
	}
	if agentText == "" {
		t.Fatalf("no agent text block: %s", summarize(seen))
	}
	for _, want := range []string{"Fixture report", "Checklist", "Numbers"} {
		if !strings.Contains(agentText, want) {
			t.Errorf("markdown section %q missing from render:\n%s", want, agentText)
		}
	}
	// The renderer must keep every line within the requested width.
	for i, line := range strings.Split(agentText, "\n") {
		if w := lipgloss.Width(line); w > 100 {
			t.Errorf("markdown line %d overflows: %d cols: %q", i, w, line)
		}
	}
}

func TestWideScenarioClips(t *testing.T) {
	s := connectFixture(t)
	_, seen := sendBlocking(t, s, "wide")
	updates := taskUpdates(seen)
	if len(updates) == 0 {
		t.Fatalf("no task updates: %s", summarize(seen))
	}
	last := updates[len(updates)-1]
	if last.State != a2a.TaskStateCompleted {
		t.Fatalf("final state = %s", last.State)
	}
	for _, b := range chat.BlocksForTask(last.Snapshot, s.Engine()) {
		if ab, ok := b.(*chat.AgentTextBlock); ok {
			for i, line := range strings.Split(ab.Render(80), "\n") {
				if w := lipgloss.Width(line); w > 80 {
					t.Errorf("wide line %d overflows 80 cols (%d): %q", i, w, line)
				}
			}
			return
		}
	}
	t.Fatal("no agent text block in the wide reply")
}

func TestHistScenario(t *testing.T) {
	s := connectFixture(t)
	_, seen := sendBlocking(t, s, "hist")
	updates := taskUpdates(seen)
	if len(updates) == 0 {
		t.Fatalf("no task updates: %s", summarize(seen))
	}
	last := updates[len(updates)-1]
	if last.State != a2a.TaskStateCompleted {
		t.Fatalf("final state = %s", last.State)
	}
	if last.Snapshot == nil {
		t.Fatal("no task snapshot on the terminal update")
	}
	if got := len(last.Snapshot.History); got < 8 {
		t.Fatalf("history length = %d, want the accumulated status messages (>= 8)", got)
	}

	// The history-length parameter truncates server-side.
	two := 2
	task, err := s.Conn().GetTask(context.Background(), string(last.Snapshot.ID), &two)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got := len(task.History); got > 2 {
		t.Fatalf("historyLength=2 returned %d messages", got)
	}

	// The rendered snapshot keeps every turn inside the width budget.
	for _, b := range chat.BlocksForTask(task, s.Engine()) {
		for i, line := range strings.Split(b.Render(80), "\n") {
			if w := lipgloss.Width(line); w > 80 {
				t.Errorf("history line %d overflows 80 cols (%d): %q", i, w, line)
			}
		}
	}
}

// TestGetTaskProtocolError exercises a genuine JSON-RPC error end to end:
// GetTask on an unknown id must surface the mapped protocol error the
// way the TUI renders it.
func TestGetTaskProtocolError(t *testing.T) {
	s := connectFixture(t)
	_, err := s.Conn().GetTask(context.Background(), "b0gus-task-id", nil)
	if err == nil {
		t.Fatal("GetTask on an unknown id unexpectedly succeeded")
	}
	friendly := agent.FriendlyError(err)
	if !strings.Contains(friendly, "TaskNotFound") && !strings.Contains(friendly, "task not found") {
		t.Fatalf("friendly error = %q, want task-not-found mapping (raw: %v)", friendly, err)
	}
}
