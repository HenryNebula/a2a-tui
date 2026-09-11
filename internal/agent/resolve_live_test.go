//go:build live

// Live card-resolution smoke tests against public agents. Never run by
// CI: they require BOTH the `live` build tag and A2A_TUI_LIVE=1, plus
// network access.
//
//	go test -tags=live -run TestResolveLive ./internal/agent/
package agent

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func liveEnabled() bool {
	return os.Getenv("A2A_TUI_LIVE") == "1"
}

func TestResolveLive(t *testing.T) {
	if !liveEnabled() {
		t.Skip("set A2A_TUI_LIVE=1 to run live resolution tests")
	}

	tests := []struct {
		url      string
		wantWire string
	}{
		{"https://agent.moneyyoureowed.com", WireV1}, // v1.0 open agent
		{"https://thehiveryiq.com", WireV03},         // v0.3 streaming agent
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			res, err := Resolve(ctx, &http.Client{Timeout: 15 * time.Second}, tt.url, Auto)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.Wire != tt.wantWire {
				t.Errorf("Wire = %q, want %q (agent may have been upgraded)", res.Wire, tt.wantWire)
			}
			if res.Summary.Name == "" || res.BaseURL == "" {
				t.Errorf("incomplete resolution: %+v", res.Summary)
			}
			t.Logf("name=%q wire=%s endpoint=%s skills=%d streaming=%v push=%v",
				res.Summary.Name, res.Wire, res.BaseURL,
				len(res.Summary.Skills),
				res.Summary.Capabilities.Streaming,
				res.Summary.Capabilities.PushNotifications)
		})
	}
}
