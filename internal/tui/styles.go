package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent = lipgloss.Color("205")
	colorDim    = lipgloss.Color("241")
	colorError  = lipgloss.Color("203")
	colorOK     = lipgloss.Color("42")

	styleHeader   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("62")).Padding(0, 1)
	styleDim      = lipgloss.NewStyle().Foreground(colorDim)
	styleHelp     = lipgloss.NewStyle().Foreground(colorDim)
	styleInputBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).BorderForeground(colorDim).Padding(0, 1)

	styleCardTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231"))
	styleCardLabel  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	styleCardValue  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleCardOK     = lipgloss.NewStyle().Foreground(colorOK)
	styleCardOff    = lipgloss.NewStyle().Foreground(colorDim)
	styleCardHeader = lipgloss.NewStyle().Bold(true).Foreground(colorDim) // table headers

	styleStatus = lipgloss.NewStyle().Foreground(colorAccent)

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
