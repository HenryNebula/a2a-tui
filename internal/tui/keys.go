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
}

var keys = keyMap{
	Quit:           key.NewBinding(key.WithKeys("ctrl+c", "ctrl+d"), key.WithHelp("ctrl+c", "quit")),
	Help:           key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Send:           key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
	Cancel:         key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel stream")),
	PaneTranscript: key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "transcript")),
	PaneCard:       key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("ctrl+g", "card")),
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
