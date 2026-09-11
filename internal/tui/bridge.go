package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// Bubble Tea bridge: a single blocking Cmd reads the session's event
// channel and delivers one event per message; App.Update re-arms it after
// processing. At most one listener is outstanding per session (armed on
// session creation and re-armed only in the agentEventMsg handler). The
// channel is never closed; shutdown is signaled by the session's Done
// channel, which the listener selects on so a torn-down session cannot
// leave it blocked forever (and cannot starve the next session's listener).

// agentEventMsg wraps one agent.Event as a tea.Msg.
type agentEventMsg struct{ ev agent.Event }

// agentEventsClosedMsg fires if the event channel is ever closed (it
// should not be); the app stops listening rather than spinning.
type agentEventsClosedMsg struct{}

// listenEvents returns a Cmd that blocks for the next session event or
// for session teardown, whichever comes first. On teardown it returns nil
// (bubbletea drops nil messages), so the listener is simply not re-armed.
func listenEvents(done <-chan struct{}, ch <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		select {
		case <-done:
			return nil
		case ev, ok := <-ch:
			if !ok {
				return agentEventsClosedMsg{}
			}
			return agentEventMsg{ev: ev}
		}
	}
}

// armListener re-arms the session listener when a session exists and no
// listener is outstanding. It returns nil otherwise.
func (a *App) armListener() tea.Cmd {
	if a.session == nil || a.listening {
		return nil
	}
	a.listening = true
	return listenEvents(a.session.Done(), a.session.Events())
}
