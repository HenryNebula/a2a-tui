package tui

import (
	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/charmbracelet/lipgloss"
)

var (
	colorAccent = lipgloss.Color("176")
	colorDim    = lipgloss.Color("245")
	colorError  = lipgloss.Color("203")
	colorOK     = lipgloss.Color("42")

	styleDim      = lipgloss.NewStyle().Foreground(colorDim)
	styleHelp     = lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
	styleInputBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colorDim).Padding(0, 1)

	styleCardTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))
	styleCardLabel  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleCardValue  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleCardOK     = lipgloss.NewStyle().Foreground(colorOK)
	styleCardOff    = lipgloss.NewStyle().Foreground(colorDim)
	styleCardHeader = lipgloss.NewStyle().Bold(true).Foreground(colorDim) // table headers

	styleStatus = lipgloss.NewStyle().Foreground(colorAccent)

	styleError = lipgloss.NewStyle().Foreground(colorError)

	styleSurfacePrompt = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
)

// styleState returns the style for the connection-state pill in the header.
func styleState(state string) lipgloss.Style {
	switch state {
	case "connected":
		return lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	case "error", "connecting":
		return lipgloss.NewStyle().Foreground(colorError).Bold(true)
	default:
		return lipgloss.NewStyle().Foreground(colorDim)
	}
}

// styleTaskState returns the pill style for a task state. Live states are
// colored (and the interactive ones bold); completed/failed keep their
// semantic color so the dashboard reads at a glance, while canceled and
// unknown states recede.
func styleTaskState(s a2a.TaskState) lipgloss.Style {
	switch s {
	case a2a.TaskStateCompleted:
		return lipgloss.NewStyle().Foreground(colorOK)
	case a2a.TaskStateFailed, a2a.TaskStateRejected:
		return lipgloss.NewStyle().Foreground(colorError)
	case a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired:
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	case a2a.TaskStateWorking:
		return lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	case a2a.TaskStateSubmitted:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	default:
		return lipgloss.NewStyle().Foreground(colorDim)
	}
}
