package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/config"
)

const testV03Card = `{
  "name": "Pane Agent",
  "description": "Renders in a pane",
  "url": "https://backend.example.com/api",
  "protocolVersion": "0.3.0",
  "capabilities": {"streaming": true, "pushNotifications": false},
  "skills": [
    {"id": "boom", "name": "Explode", "description": "Long description that will need truncating in the table cell", "tags": ["a", "b"]}
  ],
  "securitySchemes": {"k": {"type": "apiKey", "name": "X-Key", "in": "header"}}
}`

// resolveTestCard resolves a card from a throwaway server.
func resolveTestCard(t *testing.T, body string) *agent.Resolved {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/agent.json" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	res, err := agent.Resolve(t.Context(), &http.Client{Timeout: 5 * time.Second}, srv.URL, agent.Auto)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return res
}

func TestCardPaneRender(t *testing.T) {
	p := NewCardPane()
	got := p.View(80, 24)
	if !strings.Contains(got, "no agent card") {
		t.Errorf("empty pane should hint at /connect, got %q", got)
	}

	p.SetCard(resolveTestCard(t, testV03Card))
	got = p.View(80, 24)
	for _, want := range []string{"Pane Agent", "A2A 0.3", "https://backend.example.com/api", "streaming ✓", "push ✗", "boom", "Explode", "X-Key"} {
		if !strings.Contains(got, want) {
			t.Errorf("card view missing %q\nview:\n%s", want, got)
		}
	}
	// No rendered line may exceed the pane width.
	for i, line := range strings.Split(got, "\n") {
		if lipgloss.Width(line) > 80 {
			t.Errorf("line %d exceeds width 80: %q", i, line)
		}
	}
}

func TestCardPaneScrollKeys(t *testing.T) {
	p := NewCardPane()
	p.SetCard(resolveTestCard(t, testV03Card))
	p.View(40, 4) // tiny viewport → scrollable

	scrollMsgs := []tea.KeyMsg{
		{Type: tea.KeyDown}, {Type: tea.KeyPgDown}, {Type: tea.KeyEnd},
		{Type: tea.KeyUp}, {Type: tea.KeyPgUp}, {Type: tea.KeyHome},
	}
	for _, m := range scrollMsgs {
		if !p.Update(m) {
			t.Errorf("Update(%v) not handled", m.String())
		}
	}
	if p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}) {
		t.Error("plain typing key should not be consumed by card pane")
	}
}

func TestAppConnectFlowAndSave(t *testing.T) {
	res := resolveTestCard(t, testV03Card)

	store := config.Open(t.TempDir())
	app := New("http://x.example.com", agent.Auto, store)

	// Deliver a successful resolution as the async Cmd would.
	app.handleConnectResult(connectResultMsg{
		ref:      "http://x.example.com",
		url:      "http://x.example.com",
		resolved: res,
	})
	if app.connState != "connected" || app.agentName != "Pane Agent" || app.protoVer != "0.3" {
		t.Fatalf("header state = %q/%q/%q", app.connState, app.agentName, app.protoVer)
	}
	if app.conn != res {
		t.Error("conn not stored on App")
	}

	// /agent save round-trips through the store.
	app.runCommand("/agent save demo")
	f, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ag, ok := f.Agent("demo")
	if !ok || ag.URL != "http://x.example.com" || ag.Protocol != "0.3" {
		t.Fatalf("saved agent = %+v", ag)
	}

	// Saved-name expansion: name → URL + pinned protocol.
	app2 := New("", agent.Auto, store)
	url, mode := app2.expandRef("demo")
	if url != "http://x.example.com" || mode != agent.Force3 {
		t.Fatalf("expandRef = %q, %v", url, mode)
	}
	// URL-ish refs pass through untouched.
	url, mode = app2.expandRef("thehiveryiq.com")
	if url != "thehiveryiq.com" || mode != agent.Auto {
		t.Fatalf("expandRef(urlish) = %q, %v", url, mode)
	}
}

func TestAppPaneSwitch(t *testing.T) {
	app := New("", agent.Auto, config.Open(t.TempDir()))
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	model, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if model.(*App).pane != paneCard {
		t.Error("ctrl+g should switch to card pane")
	}
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if model.(*App).pane != paneTranscript {
		t.Error("ctrl+t should switch back to transcript")
	}
}
