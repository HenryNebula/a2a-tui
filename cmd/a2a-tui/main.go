// Command a2a-tui is a terminal client for agents that speak the A2A
// protocol (https://a2a-protocol.org) and render A2UI surfaces
// (https://a2ui.org).
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/config"
	"github.com/HenryNebula/a2a-tui/internal/tui"
)

func main() {
	var agentRef, protoFlag string
	flag.StringVar(&agentRef, "agent", "", "A2A agent base URL (or saved agent name) to connect to at startup")
	flag.StringVar(&protoFlag, "protocol", "auto", `wire protocol: "1.0", "0.3" or "auto" (detect from the agent card)`)
	flag.Parse()

	mode, err := agent.ParseProtocolMode(protoFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2a-tui:", err)
		os.Exit(2)
	}

	// A nil store (no usable user config dir) degrades saved-agent
	// features gracefully inside the app.
	store, _ := config.DefaultStore()

	// --agent accepts a saved agent name too; its stored URL and
	// protocol pin expand here. An explicit --protocol flag wins.
	explicitProto := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "protocol" {
			explicitProto = true
		}
	})
	if agentRef != "" && !strings.Contains(agentRef, "://") {
		if f, err := store.Load(); err == nil {
			if ag, ok := f.Agent(agentRef); ok {
				agentRef = ag.URL
				if !explicitProto && ag.Protocol != "" {
					if m, err := agent.ParseProtocolMode(ag.Protocol); err == nil {
						mode = m
					}
				}
			}
		}
	}

	prog, err := tea.NewProgram(tui.New(agentRef, mode, store), tea.WithAltScreen()).Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2a-tui:", err)
		os.Exit(1)
	}
	// Tear down the session (pumps, connection) after the UI exits.
	if app, ok := prog.(*tui.App); ok {
		app.Shutdown()
	}
}
