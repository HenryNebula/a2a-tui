package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestHelpOverlayTogglesAndRenders(t *testing.T) {
	a := newTestApp(t, nil)

	// f1 opens the overlay (with the chat input focused, ? must type
	// instead — see keys_test.go).
	update(t, a, tea.KeyMsg{Type: tea.KeyF1})
	if !a.helpOpen {
		t.Fatal("f1 did not open the help overlay")
	}
	view := stripStyle(a.View())
	for _, want := range []string{
		"a2a-tui · help",
		"keys", "commands",
		// key.Binding help data + the console binding added in M9
		// (pane chords use the compact "^x" footer spelling)
		"^e", "^w", "^k",
		// per-pane hints
		"ctrl+l", "wire: clear",
		// footer
		"f1 / esc close",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("overlay missing %q:\n%s", want, view)
		}
	}
	// The full command table lives in the cached document even when the
	// viewport clips it (the stacked layout is taller than the pane).
	if !strings.Contains(stripStyle(a.helpPane.cache), "raw JSON-RPC console") {
		t.Errorf("command table missing /console row:\n%s", a.helpPane.cache)
	}

	// The overlay covers the transcript pane.
	if !strings.Contains(view, "a2a-tui · help") {
		t.Fatal("overlay not drawn over the main pane")
	}

	// esc closes it.
	update(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.helpOpen {
		t.Fatal("esc did not close the overlay")
	}
}

func TestHelpOverlayQuestionKeyCloses(t *testing.T) {
	a := newTestApp(t, nil)
	update(t, a, tea.KeyMsg{Type: tea.KeyF1})
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if a.helpOpen {
		t.Fatal("? should close the open overlay")
	}
}

func TestHelpOverlayScrolls(t *testing.T) {
	a := newTestApp(t, nil)
	// A short, narrow window forces the stacked layout: the document
	// outgrows the viewport and scrolling has something to do.
	a.width, a.height = 46, 15
	a.layout()
	update(t, a, tea.KeyMsg{Type: tea.KeyF1})
	_ = a.View() // size the viewport before scrolling
	before := a.helpPane.viewport.YOffset
	update(t, a, tea.KeyMsg{Type: tea.KeyDown})
	if a.helpPane.viewport.YOffset <= before {
		t.Fatalf("down did not scroll the overlay (before=%d after=%d)", before, a.helpPane.viewport.YOffset)
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyHome})
	if a.helpPane.viewport.YOffset != 0 {
		t.Fatalf("home did not rewind (offset=%d)", a.helpPane.viewport.YOffset)
	}
	// The scroll key must not leak into the input box.
	if a.input.Value() != "" {
		t.Fatalf("scroll key leaked into the input: %q", a.input.Value())
	}
}

func TestHelpOverlayClosesOnOtherKeysAndRoutes(t *testing.T) {
	a := newTestApp(t, nil)
	update(t, a, tea.KeyMsg{Type: tea.KeyF1})
	// ctrl+k closes the overlay AND opens the tasks dashboard.
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlK})
	if a.helpOpen {
		t.Fatal("overlay should close when another key routes")
	}
	if a.pane != paneTasks {
		t.Fatalf("ctrl+k should still route under the overlay (pane=%v)", a.pane)
	}
}

func TestHelpCommandOpensOverlay(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.SetValue("/help")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !a.helpOpen {
		t.Fatal("/help did not open the overlay")
	}
	// The whole table is in the cached document even when the viewport
	// clips it (/quit is the last row).
	_ = a.View()
	if !strings.Contains(stripStyle(a.helpPane.cache), "/quit") {
		t.Fatal("/quit missing from the command table")
	}
}

func TestCommandHelpCoversDispatcher(t *testing.T) {
	// Every command the dispatcher knows must have a help row (the table
	// and the switch live in different files; drift is easy).
	dispatched := []string{
		"/help", "/quit", "/exit", "/connect", "/disconnect", "/agents", "/agent",
		"/card", "/tasks", "/push", "/surface", "/chat", "/stream", "/wire",
		"/task", "/cancel", "/history", "/clear", "/console",
	}
	known := map[string]bool{}
	for _, c := range commandHelp {
		known[c.name] = true
	}
	for _, name := range dispatched {
		if !known[name] && name != "/exit" { // /exit is a /quit alias
			t.Errorf("command %q missing from commandHelp", name)
		}
	}
}
