package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// callSoon runs a tea.Cmd in a goroutine and delivers its message, failing
// the test if it does not return within timeout (a blocked listener hangs
// here instead of leaking silently).
func callSoon(t *testing.T, cmd tea.Cmd, timeout time.Duration) tea.Msg {
	t.Helper()
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(timeout):
		t.Fatalf("listener did not return within %s", timeout)
		return nil
	}
}

// TestListenEventsUnblocksOnTeardown: the bridge listener must exit when
// the session's Done channel closes instead of blocking on the event
// channel forever (a leaked listener also pins a.listening and starves the
// next session's listener).
func TestListenEventsUnblocksOnTeardown(t *testing.T) {
	s := agent.NewSession(nil)
	shutdown := make(chan struct{})
	go func() {
		s.Shutdown()
		close(shutdown)
	}()

	if msg := callSoon(t, listenEvents(s.Done(), s.Events()), 2*time.Second); msg != nil {
		t.Fatalf("teardown must yield a nil message, got %#v", msg)
	}
	select {
	case <-shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not finish")
	}
}

// TestListenEventsDeliversEvents: while the session lives, events flow and
// a closed event channel reports agentEventsClosedMsg.
func TestListenEventsDeliversEvents(t *testing.T) {
	s := agent.NewSession(nil)

	// The events channel is receive-only outside the package, so drive the
	// listener with a stand-in channel of the same shape.
	ch := make(chan agent.Event, 1)
	ch <- agent.StatusLineEvent{Text: "hello"}
	msg := callSoon(t, listenEvents(s.Done(), ch), time.Second)
	em, ok := msg.(agentEventMsg)
	if !ok {
		t.Fatalf("want agentEventMsg, got %#v", msg)
	}
	if sl, ok := em.ev.(agent.StatusLineEvent); !ok || sl.Text != "hello" {
		t.Fatalf("unexpected event: %#v", em.ev)
	}

	close(ch)
	if msg := callSoon(t, listenEvents(s.Done(), ch), time.Second); msg != (agentEventsClosedMsg{}) {
		t.Fatalf("closed channel must report agentEventsClosedMsg, got %#v", msg)
	}
}

// The two tests below reproduce issue #34's user-facing half: after
// /disconnect (or a second /connect), the old session's blocked listener
// pinned a.listening=true, so the NEXT session's listener never armed and
// the app rendered nothing the new agent sent.

// doneWithin waits for a session's Done channel for up to timeout
// (Shutdown is dispatched asynchronously by the app).
func doneWithin(done <-chan struct{}, timeout time.Duration) bool {
	deadline := time.After(timeout)
	select {
	case <-done:
		return true
	case <-deadline:
		return false
	}
}

// TestDisconnectRearmsListenerForNextSession: /disconnect must clear the
// listening latch and tear the old session down.
func TestDisconnectRearmsListenerForNextSession(t *testing.T) {
	a := newTestApp(t, nil)
	old := a.session
	if a.armListener() == nil {
		t.Fatal("precondition: listener should arm for a live session")
	}

	a.runCommand("/disconnect")

	if a.listening {
		t.Fatal("a.listening must reset on /disconnect or the next session's listener never arms")
	}
	if a.session != nil {
		t.Fatal("a.session must be nil after /disconnect")
	}
	if !doneWithin(old.Done(), 2*time.Second) {
		t.Fatal("old session must be shut down by /disconnect")
	}
}

// TestConnectReplacementShutsDownOldSession: connecting while already
// connected must fully Shut down the replaced session (not just StopPush)
// and arm the new session's listener.
func TestConnectReplacementShutsDownOldSession(t *testing.T) {
	a := newTestApp(t, nil)
	old := a.session
	if a.armListener() == nil {
		t.Fatal("precondition: listener should arm for a live session")
	}

	fresh := agent.NewSession(&fakeConn{})
	t.Cleanup(fresh.Shutdown)
	a.handleConnectResult(connectResultMsg{
		ref:      "http://x.example.com",
		url:      "http://x.example.com",
		resolved: resolveTestCard(t, testV03Card),
		session:  fresh,
	})

	if a.session != fresh {
		t.Fatal("new session not installed")
	}
	if !a.listening {
		t.Fatal("listener must be armed for the replacement session")
	}
	if !doneWithin(old.Done(), 2*time.Second) {
		t.Fatal("replaced session must be shut down, not just StopPush")
	}
}
