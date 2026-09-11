package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// Bubble Tea bridge: a single blocking Cmd reads the session's event
// channel and delivers one event per message; App.Update re-arms it after
// processing. Exactly one listener is outstanding at any time (armed on
// session creation and re-armed only in the agentEventMsg handler), and
// the channel is never closed — shutdown is signaled by canceling the
// session, after which the listener is no longer re-armed.

// agentEventMsg wraps one agent.Event as a tea.Msg.
type agentEventMsg struct{ ev agent.Event }

// agentEventsClosedMsg fires if the event channel is ever closed (it
// should not be); the app stops listening rather than spinning.
type agentEventsClosedMsg struct{}

// listenEvents returns a Cmd that blocks for the next session event.
func listenEvents(ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return agentEventsClosedMsg{}
		}
		return agentEventMsg{ev: ev}
	}
}

// armListener re-arms the session listener when a session exists and no
// listener is outstanding. It returns nil otherwise.
func (a *App) armListener() tea.Cmd {
	if a.session == nil || a.listening {
		return nil
	}
	a.listening = true
	return listenEvents(a.session.Events())
}
