package tui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// echoJSONServer answers any POST with a small JSON body.
func echoJSONServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`))
	}))
	return srv
}

// fakeEntries builds wire-log entries with JSON bodies.
func fakeEntries() []wirelog.Entry {
	req, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1", "method": "message/send",
		"params": map[string]any{"message": map[string]any{"text": "hi"}},
	})
	resp, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": "1",
		"result": map[string]any{"id": "t-1", "status": map[string]any{"state": "completed"}},
	})
	at := time.Date(2026, 9, 11, 14, 3, 22, 0, time.UTC)
	return []wirelog.Entry{{
		At:          at,
		Method:      "POST",
		URL:         "https://agent.example.com/rpc",
		ReqBody:     req,
		Duration:    128 * time.Millisecond,
		Status:      200,
		RespHeaders: httpHeader("Content-Type", "application/json"),
		RespBody:    resp,
	}}
}

// httpHeader builds a single-entry header map without importing net/http
// in every test file.
func httpHeader(k, v string) map[string][]string { return map[string][]string{k: {v}} }

func TestWirePaneRendersFrames(t *testing.T) {
	p := NewWirePane()
	p.Sync(fakeEntries())
	view := stripStyle(p.View(90, 30))

	for _, want := range []string{
		"POST", "https://agent.example.com/rpc", "14:03:22",
		"\"method\": \"message/send\"", "200", "application/json",
		"\"state\": \"completed\"",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("wire pane missing %q\nview:\n%s", want, view)
		}
	}
	// Pretty-printed JSON must be indented under the frame header.
	if !strings.Contains(view, "\n  {") {
		t.Errorf("body not pretty-printed:\n%s", view)
	}
}

func TestWirePaneErrorEntry(t *testing.T) {
	p := NewWirePane()
	p.Sync([]wirelog.Entry{{
		At: time.Now(), Method: "POST", URL: "http://x/", Err: "dial tcp: refused",
	}})
	view := stripStyle(p.View(80, 10))
	if !strings.Contains(view, "POST") || !strings.Contains(view, "refused") {
		t.Fatalf("error frame missing details:\n%s", view)
	}
}

func TestWirePaneTruncatesBigBodies(t *testing.T) {
	big := bytes.Repeat([]byte(`{"pad":"aaaaaaaaaa"},`), 1024) // ~20KB
	p := NewWirePane()
	p.Sync([]wirelog.Entry{{At: time.Now(), Method: "POST", URL: "http://x/", ReqBody: big}})
	view := stripStyle(p.View(80, 40))
	if !strings.Contains(view, "(truncated)") {
		t.Fatalf("truncation marker missing (rendered %d chars)", len(view))
	}
}

func TestWirePaneClearKey(t *testing.T) {
	p := NewWirePane()
	p.Sync(fakeEntries())
	if view := stripStyle(p.View(90, 20)); !strings.Contains(view, "message/send") {
		t.Fatalf("frame missing before clear:\n%s", view)
	}
	if !p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}) {
		t.Fatal("c should be consumed by the wire pane")
	}
	view := stripStyle(p.View(90, 20))
	if strings.Contains(view, "message/send") {
		t.Fatalf("frame survived clear:\n%s", view)
	}
	if !strings.Contains(view, "cleared") {
		t.Fatalf("cleared hint missing:\n%s", view)
	}
}

func TestWirePaneScrollKeys(t *testing.T) {
	p := NewWirePane()
	p.Sync(fakeEntries())
	scrolls := []tea.KeyMsg{
		{Type: tea.KeyDown}, {Type: tea.KeyPgDown}, {Type: tea.KeyEnd},
		{Type: tea.KeyUp}, {Type: tea.KeyPgUp}, {Type: tea.KeyHome},
	}
	for _, m := range scrolls {
		if !p.Update(m) {
			t.Errorf("Update(%v) not consumed", m.String())
		}
	}
	if p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}) {
		t.Error("typing keys must fall through to the input box")
	}
}

func TestWirePaneEmpty(t *testing.T) {
	p := NewWirePane()
	view := stripStyle(p.View(80, 10))
	if !strings.Contains(view, "wire log empty") {
		t.Fatalf("empty hint missing:\n%s", view)
	}
}

func TestAppWirePaneSwitchAndCommand(t *testing.T) {
	a := newTestApp(t, nil)
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlW})
	if a.pane != paneWire {
		t.Fatal("ctrl+w should open the wire pane")
	}

	a.pane = paneTranscript
	a.wireLog.SetEnabled(false)
	a.input.SetValue("/wire")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.pane != paneWire {
		t.Fatal("/wire should open the pane")
	}
	if !a.wireLog.Enabled() {
		t.Fatal("/wire should also enable capture")
	}

	// While the wire pane is focused, "c" clears instead of typing.
	a.wirePane.Sync(fakeEntries())
	a.input.Reset()
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if a.input.Value() != "" {
		t.Fatalf("c leaked into the input: %q", a.input.Value())
	}

	// /wire off disables capture but keeps the pane readable.
	a.input.SetValue("/wire off")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.wireLog.Enabled() {
		t.Fatal("/wire off should disable capture")
	}
}

// TestWirePaneShowsLiveCapture drives one real HTTP exchange through the
// app's wirelog transport and asserts it renders.
func TestWirePaneShowsLiveCapture(t *testing.T) {
	a := newTestApp(t, nil)
	srv := echoJSONServer(t)
	defer srv.Close()

	client := a.httpClient
	client.Transport = a.wireLog.Transport(nil)
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"ping":1}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	// The wirelog entry commits when the response body hits EOF/Close, so
	// drain it before rendering.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	a.pane = paneWire
	view := stripStyle(a.View())
	if !strings.Contains(view, "POST") || !strings.Contains(view, srv.URL) {
		t.Fatalf("captured exchange not rendered:\n%s", view)
	}
	if !strings.Contains(view, `"ping": 1`) {
		t.Fatalf("pretty-printed request body missing:\n%s", view)
	}
}
