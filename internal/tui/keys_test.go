package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// quits reports whether running cmd yields a (possibly batched) quit.
func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	switch msg := cmd().(type) {
	case tea.QuitMsg:
		return true
	case tea.BatchMsg:
		for _, c := range msg {
			if quits(c) {
				return true
			}
		}
	}
	return false
}

// The chat input owns the keyboard in the transcript pane, so "?" must be
// inserted, not interpreted as the help toggle.
func TestQuestionMarkInsertsIntoFocusedInput(t *testing.T) {
	a := newTestApp(t, nil)
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("wh?")})
	if a.helpOpen {
		t.Fatal("? toggled help while the chat input had focus")
	}
	if a.input.Value() != "wh?" {
		t.Fatalf("? not inserted into the input: %q", a.input.Value())
	}
}

// With no text input focused, "?" keeps its documented toggle meaning.
func TestQuestionMarkTogglesHelpWhenNoInputFocused(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.Blur()
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !a.helpOpen {
		t.Fatal("? did not toggle help with no input focused")
	}
}

// f1 is the always-available help toggle.
func TestF1TogglesHelp(t *testing.T) {
	a := newTestApp(t, nil)
	update(t, a, tea.KeyMsg{Type: tea.KeyF1})
	if !a.helpOpen {
		t.Fatal("f1 did not open the help overlay with the input focused")
	}
	update(t, a, tea.KeyMsg{Type: tea.KeyF1})
	if a.helpOpen {
		t.Fatal("f1 did not close the open help overlay")
	}
}

// ctrl+d belongs to the focused textarea (forward delete) — it must not
// quit the app mid-sentence. ctrl+c stays the quit chord.
func TestCtrlDDeletesForwardInsteadOfQuitting(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.SetValue("abc")
	a.input.CursorStart()
	if quits(update(t, a, tea.KeyMsg{Type: tea.KeyCtrlD})) {
		t.Fatal("ctrl+d quit the app while the input had focus")
	}
	if v := a.input.Value(); v != "bc" {
		t.Fatalf("ctrl+d did not forward-delete: %q", v)
	}
}

func TestCtrlCStillQuits(t *testing.T) {
	a := newTestApp(t, nil)
	if !quits(update(t, a, tea.KeyMsg{Type: tea.KeyCtrlC})) {
		t.Fatal("ctrl+c must quit")
	}
}
