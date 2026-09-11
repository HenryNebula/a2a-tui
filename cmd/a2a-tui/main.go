// Command a2a-tui is a terminal client for agents that speak the A2A
// protocol (https://a2a-protocol.org) and render A2UI surfaces
// (https://a2ui.org).
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/tui"
)

func main() {
	var agentURL string
	flag.StringVar(&agentURL, "agent", "", "A2A agent base URL (or saved agent name) to connect to at startup")
	flag.Parse()

	if _, err := tea.NewProgram(tui.New(agentURL), tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "a2a-tui:", err)
		os.Exit(1)
	}
}
