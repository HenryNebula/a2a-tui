package tui

import (
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
)

// keyMap holds the application-wide bindings. Per-view bindings live with
// their views; global keys are matched before routing.
type keyMap struct {
	Quit           key.Binding
	Help           key.Binding
	Send           key.Binding
	Cancel         key.Binding
	PaneTranscript key.Binding
	PaneCard       key.Binding
	PaneSurface    key.Binding
	PaneTasks      key.Binding
	PaneWire       key.Binding
	PaneConsole    key.Binding
	WireClear      key.Binding
}

var keys = keyMap{
	// ctrl+d is left to the focused textarea (its forward-delete binding);
	// ctrl+c stays the quit chord.
	Quit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	// f1 toggles help (and /help). A bare "?" must never be a binding:
	// single-character globals fire while typing — "?" is a character
	// people put in messages.
	Help:           key.NewBinding(key.WithKeys("f1"), key.WithHelp("f1", "help")),
	Send:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	Cancel:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	PaneTranscript: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("^t", "chat")),
	PaneCard:       key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("^g", "card")),
	PaneSurface:    key.NewBinding(key.WithKeys("ctrl+f"), key.WithHelp("^f", "a2ui")),
	PaneTasks:      key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("^k", "tasks")),
	PaneWire:       key.NewBinding(key.WithKeys("ctrl+w"), key.WithHelp("^w", "wire")),
	PaneConsole:    key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("^e", "console")),
	WireClear:      key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "wire: clear")),
}

// keyMatches reports whether msg presses the binding.
func keyMatches(msg tea.KeyMsg, b key.Binding) bool {
	for _, k := range b.Keys() {
		if msg.String() == k {
			return true
		}
	}
	return false
}
