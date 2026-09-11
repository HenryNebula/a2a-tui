package tui

import (
	"fmt"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// Messages produced by async work (Cmds, and later the agent session's
// event channel) and consumed by App.Update.

// statusMsg is an informational message shown in the transcript.
type statusMsg string

// errMsg surfaces an error to the user.
type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

// connectStatusMsg reports connection lifecycle changes.
type connectStatusMsg struct {
	agentName string
	protoVer  string
	state     string // "connecting", "connected", "error"
	err       error
}

func (m connectStatusMsg) String() string {
	if m.err != nil {
		return fmt.Sprintf("%s: %v", m.state, m.err)
	}
	return m.state
}

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
	// err is set on failure.
	err error
}
