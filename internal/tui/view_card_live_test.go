//go:build live

// Live card-pane render smoke test. Never run by CI: requires the
// `live` build tag and A2A_TUI_LIVE=1, plus network access. Run with
//
//	A2A_TUI_LIVE=1 go test -tags=live -v -run TestCardPaneLive ./internal/tui/
package tui

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

func TestCardPaneLive(t *testing.T) {
	if os.Getenv("A2A_TUI_LIVE") != "1" {
		t.Skip("set A2A_TUI_LIVE=1 to run live card-pane tests")
	}

	for _, url := range []string{
		"https://agent.moneyyoureowed.com", // v1.0
		"https://thehiveryiq.com",          // v0.3
	} {
		t.Run(url, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			res, err := agent.Resolve(ctx, &http.Client{Timeout: 15 * time.Second}, url, agent.Auto)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			p := NewCardPane()
			p.SetCard(res)
			view := p.View(100, 40)
			for _, want := range []string{res.Summary.Name, "A2A " + res.Wire} {
				if !strings.Contains(view, want) {
					t.Errorf("rendered card missing %q", want)
				}
			}
			// Dump with styling stripped for human eyeballing with -v.
			t.Logf("card pane for %s:\n%s", url, ansi.Strip(view))
		})
	}
}
