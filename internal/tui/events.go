package tui

import (
	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// Messages produced by async work (Cmds and the session event bridge)
// and consumed by App.Update.

// connectResultMsg carries the outcome of an async agent-card resolution
// started by /connect or the --agent flag.
type connectResultMsg struct {
	// ref is what the user asked to connect to (name or URL).
	ref string
	// url is the concrete URL the resolver used (ref expanded through
	// saved agents when applicable).
	url string
	// resolved is set on success.
	resolved *agent.Resolved
	// session is the live chat session, set on success.
	session *agent.Session
	// err is set on failure.
	err error
}

// taskResultMsg carries the outcome of a one-shot task RPC (GetTask,
// CancelTask, compaction refresh).
type taskResultMsg struct {
	// op names the operation for status lines ("task", "cancel",
	// "history", "refresh").
	op string
	// id is the task ID the op targeted.
	id string
	// task is set on success.
	task *a2a.Task
	// err is set on failure.
	err error
}
