package tui

import (
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

func TestPushCommandOnOff(t *testing.T) {
	a := newTestApp(t, nil)

	// /push on boots the webhook and registers future tasks.
	a.input.SetValue("/push on")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !a.session.PushEnabled() {
		t.Fatal("/push on did not enable push")
	}
	url := a.session.PushURL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/push") {
		t.Fatalf("push URL = %q", url)
	}
	out := rendered(a)
	if !strings.Contains(out, "webhook "+url) || !strings.Contains(out, "⇄ push") {
		t.Fatalf("push transcript notes missing:\n%s", out)
	}
	if !strings.Contains(out, "tunnel") {
		t.Fatalf("tunnel hint missing:\n%s", out)
	}
	// Header shows the push indicator while connected.
	if !strings.Contains(stripStyle(a.View()), "· push") {
		t.Fatalf("header push indicator missing:\n%s", stripStyle(a.View()))
	}

	// A push-delivered update (Source:"push" — the webhook path is covered
	// by the agent and pushsrv tests) lands badged in the transcript.
	update(t, a, agentEventMsg{ev: agent.TaskUpdateEvent{
		TaskID: "t-push", State: a2a.TaskStateWorking,
		StatusText: "via webhook", Source: "push",
	}})
	transcript := rendered(a)
	if !strings.Contains(transcript, "⇄ push") {
		t.Fatalf("push badge missing from the pill:\n%s", transcript)
	}
	if !strings.Contains(transcript, "via webhook") {
		t.Fatalf("push status text missing:\n%s", transcript)
	}

	// /push off stops the webhook and clears the indicator.
	a.input.SetValue("/push off")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.session.PushEnabled() || a.session.PushURL() != "" {
		t.Fatal("/push off did not stop push")
	}
	if strings.Contains(stripStyle(a.View()), "· push") {
		t.Fatal("push indicator survived /push off")
	}
}

func TestPushStatusAndUsage(t *testing.T) {
	a := newTestApp(t, nil)

	a.input.SetValue("/push")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(rendered(a), "push off") {
		t.Fatalf("status line missing:\n%s", rendered(a))
	}

	a.input.SetValue("/push sideways")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(rendered(a), "usage: /push") {
		t.Fatalf("usage hint missing:\n%s", rendered(a))
	}
}

func TestPushCommandWithoutSession(t *testing.T) {
	a := New("", agent.Auto, nil)
	a.width, a.height = 80, 24
	a.ready = true
	a.input.SetValue("/push on")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(rendered(a), "not connected") {
		t.Fatalf("expected not-connected note:\n%s", rendered(a))
	}
}
