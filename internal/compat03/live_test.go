//go:build live

// Live end-to-end check against real 0.3 agents. Run with:
//
//	A2A_TUI_LIVE=1 go test -tags live -run TestLive ./internal/compat03/
//
// Only reads (card fetch) plus one benign message per agent are sent;
// agents are expected to be intermittently down, so failures here are
// informational — the observed wire traffic is the point.
package compat03_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// liveAgents lists 0.3 agents worth probing, in order.
var liveAgents = []string{
	"https://thehiveryiq.com",
	"https://api.rosentic.com",
	"https://agent.humanbrowser.cloud",
}

func TestLiveResolveAndStream(t *testing.T) {
	if os.Getenv("A2A_TUI_LIVE") == "" {
		t.Skip("set A2A_TUI_LIVE=1 to run live agent checks")
	}

	for _, target := range liveAgents {
		t.Run(target, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()

			res, err := agent.Resolve(ctx, nil, target, agent.Auto)
			if err != nil {
				t.Logf("resolve failed (agent may be down): %v", err)
				return
			}
			t.Logf("resolved: wire=%s base=%s card=%s", res.Wire, res.BaseURL, res.Summary.Name)
			if res.Wire != agent.WireV03 {
				t.Logf("agent no longer speaks 0.3 (now %s); skipping", res.Wire)
				return
			}

			conn, err := agent.NewConn(ctx, res, nil)
			if err != nil {
				t.Fatalf("NewConn: %v", err)
			}
			defer conn.Destroy()
			t.Logf("connected: wire=%s base=%s streaming=%v push=%v",
				conn.WireVersion(), conn.BaseURL(),
				res.Summary.Capabilities.Streaming, res.Summary.Capabilities.PushNotifications)

			req := &a2a.SendMessageRequest{
				Message: a2a.NewMessage(a2a.MessageRoleUser,
					a2a.NewTextPart("Reply with the single word: pong.")),
			}
			n := 0
			for ev, err := range conn.SendStreamingMessage(ctx, req) {
				if err != nil {
					t.Logf("stream ended with error: %v", err)
					break
				}
				n++
				switch e := ev.(type) {
				case *a2a.Task:
					fmt.Fprintf(os.Stderr, "  [task] %s state=%s artifacts=%d\n", e.ID, e.Status.State, len(e.Artifacts))
				case *a2a.TaskStatusUpdateEvent:
					msg := ""
					if e.Status.Message != nil && len(e.Status.Message.Parts) > 0 {
						msg = e.Status.Message.Parts[0].Text()
					}
					fmt.Fprintf(os.Stderr, "  [status] %s state=%s final-msg=%q\n", e.TaskID, e.Status.State, msg)
				case *a2a.TaskArtifactUpdateEvent:
					fmt.Fprintf(os.Stderr, "  [artifact] task=%s id=%s append=%v lastChunk=%v\n",
						e.TaskID, e.Artifact.ID, e.Append, e.LastChunk)
				case *a2a.Message:
					fmt.Fprintf(os.Stderr, "  [message] role=%s parts=%d\n", e.Role, len(e.Parts))
				}
				if n > 50 {
					t.Log("stopping after 50 events")
					break
				}
			}
			t.Logf("observed %d events", n)
		})
	}
}
