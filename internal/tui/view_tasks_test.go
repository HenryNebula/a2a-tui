package tui

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// seedTasks feeds task events through the app exactly as the session
// bridge would. The session's pumps normally populate the registry before
// events reach the UI, so the seeds go to both places.
func seedTasks(t *testing.T, a *App) {
	t.Helper()
	seeds := []struct {
		id    string
		state a2a.TaskState
		text  string
	}{
		{"t-old", a2a.TaskStateCompleted, "first task done"},
		{"t-live", a2a.TaskStateWorking, "still running"},
		{"t-input", a2a.TaskStateInputRequired, "needs input"},
	}
	for _, s := range seeds {
		info := a2a.TaskInfo{TaskID: a2a.TaskID(s.id), ContextID: "c"}
		msg := a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart(s.text))
		a.session.Registry().Observe(a2a.NewStatusUpdateEvent(info, s.state, msg))
		source := "stream"
		if s.id == "t-input" {
			source = "push"
		}
		update(t, a, agentEventMsg{ev: agent.TaskUpdateEvent{
			TaskID: s.id, ContextID: "c", State: s.state, StatusText: s.text, Source: source,
		}})
	}
}

func TestTasksPaneRendersRegistry(t *testing.T) {
	a := newTestApp(t, nil)
	seedTasks(t, a)
	a.openTasksPane()
	if a.pane != paneTasks {
		t.Fatal("openTasksPane did not switch panes")
	}

	view := a.View()
	for _, want := range []string{"t-old", "t-live", "t-input", "completed", "working", "input-required", "needs input"} {
		if !strings.Contains(stripStyle(view), want) {
			t.Errorf("dashboard missing %q\nview:\n%s", want, view)
		}
	}
	// Registry order is newest-first: the input-required task leads.
	if strings.Index(view, "t-input") > strings.Index(view, "t-old") {
		t.Errorf("tasks not sorted by updatedAt desc:\n%s", view)
	}
	// No dashboard line may exceed the pane width.
	body := a.tasksPane.View(a.width-2, 20)
	for i, line := range strings.Split(body, "\n") {
		if lipgloss.Width(line) > a.width-2 {
			t.Errorf("dashboard line %d exceeds width %d: %q", i, a.width-2, line)
		}
	}
}

// stripStyle drops ANSI sequences so plain substring checks work.
func stripStyle(s string) string {
	out := make([]rune, 0, len(s))
	inEscape := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEscape = true
		case inEscape:
			if r == 'm' {
				inEscape = false
			}
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

func TestTasksPaneCursorAndKeys(t *testing.T) {
	a := newTestApp(t, nil)
	seedTasks(t, a)
	a.openTasksPane()

	if got := a.tasksPane.SelectedID(); got != "t-input" {
		t.Fatalf("initial selection = %q, want newest task", got)
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyDown})
	if got := a.tasksPane.SelectedID(); got != "t-live" {
		t.Fatalf("after down = %q", got)
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyUp}) // back to the top
	update(t, a, tea.KeyMsg{Type: tea.KeyUp}) // clamps at the top
	if got := a.tasksPane.SelectedID(); got != "t-input" {
		t.Fatalf("after clamped up = %q, want t-input", got)
	}
	// Bottom clamp: two downs reach the oldest task, a third stays.
	update(t, a, tea.KeyMsg{Type: tea.KeyEnd})
	if got := a.tasksPane.SelectedID(); got != "t-old" {
		t.Fatalf("end = %q, want t-old", got)
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyDown})
	if got := a.tasksPane.SelectedID(); got != "t-old" {
		t.Fatalf("down past end = %q, want t-old (clamped)", got)
	}

	// Esc leaves the pane; typing falls back to the input box afterwards.
	update(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.pane != paneTranscript {
		t.Fatal("esc should leave the dashboard")
	}
}

func TestTasksPaneEnterOpensDetail(t *testing.T) {
	a := newTestApp(t, nil)
	seedTasks(t, a)
	a.openTasksPane()
	update(t, a, tea.KeyMsg{Type: tea.KeyDown}) // select t-live
	update(t, a, tea.KeyMsg{Type: tea.KeyDown}) // select t-old? ordering: [t-input, t-live, t-old]

	cmd := update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should fetch the task detail")
	}
	msg := cmd()
	res, ok := msg.(taskResultMsg)
	if !ok || res.task == nil {
		t.Fatalf("enter produced %T", msg)
	}
	update(t, a, msg)

	if !a.tasksPane.DetailOpen() {
		t.Fatal("detail view not open after fetch")
	}
	view := stripStyle(a.View())
	for _, want := range []string{"the question", "the answer", "completed"} {
		if !strings.Contains(view, want) {
			t.Errorf("detail missing %q:\n%s", want, view)
		}
	}

	// Esc from the detail returns to the list, a second esc leaves the pane.
	update(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.tasksPane.DetailOpen() || a.pane != paneTasks {
		t.Fatalf("esc should close the detail, not the pane (detail=%v pane=%v)", a.tasksPane.DetailOpen(), a.pane)
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.pane != paneTranscript {
		t.Fatal("second esc should leave the dashboard")
	}
}

func TestTasksPaneCancelKey(t *testing.T) {
	a := newTestApp(t, nil)
	seedTasks(t, a)
	a.openTasksPane()

	cmd := update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if cmd == nil {
		t.Fatal("c should cancel the selected task")
	}
	msg := cmd()
	res, ok := msg.(taskResultMsg)
	if !ok || res.op != "cancel" || res.err == nil {
		t.Fatalf("c produced %#v", msg)
	}
}

func TestTasksPaneSubscribeAndRefreshKeys(t *testing.T) {
	a := newTestApp(t, nil)
	seedTasks(t, a)
	a.openTasksPane()
	id := a.tasksPane.SelectedID()

	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !strings.Contains(rendered(a), "subscribing to task "+id) {
		t.Fatalf("subscribe hint missing:\n%s", rendered(a))
	}
	// The subscribe op is live: CancelActive must see it.
	if n := a.session.CancelActive(); n == 0 {
		t.Fatal("subscribe did not register an operation")
	}

	cmd := update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("r should refresh")
	}
	if !strings.Contains(a.statusText, "refreshing") {
		t.Fatalf("status = %q", a.statusText)
	}
}

func TestTasksPaneEnterDoesNotSubmitInput(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)
	seedTasks(t, a)
	a.openTasksPane()
	a.input.SetValue("this should not send")

	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if conn.getSends() != 0 {
		t.Fatalf("enter in the dashboard must not send, got %d sends", conn.getSends())
	}
	if strings.Contains(rendered(a), "this should not send") {
		t.Fatal("input leaked into the transcript from the dashboard")
	}
}

func TestTasksPaneEmpty(t *testing.T) {
	a := newTestApp(t, nil)
	a.openTasksPane()
	view := stripStyle(a.View())
	if !strings.Contains(view, "no tasks observed") {
		t.Fatalf("empty hint missing:\n%s", view)
	}
}

func TestTasksCommandAndCtrlK(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.SetValue("/tasks")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.pane != paneTasks {
		t.Fatal("/tasks did not open the dashboard")
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlT})
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlK})
	if a.pane != paneTasks {
		t.Fatal("ctrl+k did not reopen the dashboard")
	}
}
