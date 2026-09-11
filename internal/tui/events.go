package tui

import "fmt"

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
