// Command a2a-fixture-agent runs the a2a-tui fixture agent: a
// deterministic A2A 1.0 server scripted by the first keyword of each user
// message (see internal/fixtureagent).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/HenryNebula/a2a-tui/internal/fixtureagent"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8877", "listen address as host:port")
	port := flag.Int("port", -1, "port override (0 binds an ephemeral port); by default the port from --addr is used")
	flag.Parse()

	host, portStr, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatalf("--addr must be host:port: %v", err)
	}
	listenPort := *port
	if listenPort < 0 {
		listenPort, err = strconv.Atoi(portStr)
		if err != nil {
			log.Fatalf("--addr port must be numeric: %v", err)
		}
	}

	baseURL, shutdown, err := fixtureagent.Start(host, listenPort)
	if err != nil {
		log.Fatalf("start fixture agent: %v", err)
	}

	fmt.Printf("a2a-tui fixture agent %s (A2A %s over JSON-RPC)\n", "v1.0", "1.0")
	fmt.Printf("listening:   %s\n", baseURL)
	fmt.Printf("agent card:  %s/.well-known/agent-card.json\n\n", baseURL)
	fmt.Print(fixtureagent.HelpText())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	fmt.Println("shutting down")
	shutdown()
}
