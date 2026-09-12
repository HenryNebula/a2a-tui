package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/chat"
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

// "?" is never a binding, in any focus state: single-character globals
// fire while typing and "?" is an ordinary character. Help is f1//help.
func TestQuestionMarkNeverTogglesHelp(t *testing.T) {
	a := newTestApp(t, nil)
	a.input.Blur()
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if a.helpOpen {
		t.Fatal("? toggled help even blurred")
	}
}

// Arrows with text in the input edit the input, not the viewport; with an
// empty input they scroll the transcript (typing vs browsing states).
func TestArrowArbitration(t *testing.T) {
	a := newTestApp(t, nil)
	// Long content so the viewport can actually scroll.
	for i := 0; i < 40; i++ {
		a.transcript.Append(chat.NewStatusBlock(fmt.Sprintf("line %d", i)))
	}
	a.refreshTranscript()
	a.transcriptV.GotoBottom()

	a.input.SetValue("typing")
	before := a.transcriptV.YOffset
	update(t, a, tea.KeyMsg{Type: tea.KeyUp})
	if a.transcriptV.YOffset != before {
		t.Fatal("Up scrolled the viewport while the input had text")
	}

	a.input.SetValue("")
	update(t, a, tea.KeyMsg{Type: tea.KeyUp})
	if a.transcriptV.YOffset == before {
		t.Fatal("Up did not scroll the viewport with an empty input")
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

// New blocks that arrive while the user is scrolled up are counted on
// the status row instead of yanking the reading position; returning to
// the bottom clears the count.
func TestPendingNewIndicator(t *testing.T) {
	a := newTestApp(t, nil)
	for i := 0; i < 40; i++ {
		a.transcript.Append(chat.NewStatusBlock(fmt.Sprintf("line %d", i)))
	}
	a.refreshTranscript()
	a.transcriptV.GotoBottom()
	// Scroll up.
	update(t, a, tea.KeyMsg{Type: tea.KeyUp})
	if a.transcriptV.AtBottom() {
		t.Fatal("precondition: not scrolled up")
	}
	// Two new blocks arrive.
	a.transcript.Append(chat.NewStatusBlock("fresh 1"))
	a.transcript.Append(chat.NewStatusBlock("fresh 2"))
	a.refreshTranscript()
	if a.transcriptV.AtBottom() {
		t.Fatal("auto-follow yanked the viewport to the bottom")
	}
	if !strings.Contains(a.statusLineView(), "2 new") {
		t.Fatalf("status = %q, want unread count", stripStyle(a.statusLineView()))
	}
	// Jump back to the bottom: count clears.
	update(t, a, tea.KeyMsg{Type: tea.KeyEnd})
	if a.pendingNew != 0 {
		t.Fatalf("pendingNew = %d after returning to bottom", a.pendingNew)
	}
}
