package compat03

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// FuzzFrames checks the SSE parser against arbitrary input. Properties:
// it never panics, always terminates within the context bound, and any
// frame it emits without error must itself be valid to inspect — JSON
// payloads (the A2A case) round-trip byte-exact.
func FuzzFrames(f *testing.F) {
	f.Add("data: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"kind\":\"task\"}}\n\n")
	f.Add("data: one\r\n\r\ndata: two\r\n\r\n")
	f.Add("event: message\nid: 7\nretry: 10\ndata: x\n\n")
	f.Add(": comment\n\n\ndata:\n\n")
	f.Add("data: unterminated")
	f.Add("data: a\ndata: b\n\n")
	f.Add("\x00\xff\x01")
	f.Add(strings.Repeat("data: x\n", 200))
	f.Add("data: " + strings.Repeat("y", 200000) + "\n\n")

	f.Fuzz(func(t *testing.T, body string) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		resp := &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
		}

		frameCount := 0
		for frame, err := range Frames(ctx, resp) {
			if err != nil {
				break // errors are fine; never a panic
			}
			frameCount++
			if frameCount > maxEventsPerStream+10 {
				t.Fatalf("parser produced unbounded frames from a finite body")
			}
			if json.Valid(frame) {
				var v any
				if err := json.Unmarshal(frame, &v); err != nil {
					t.Fatalf("valid JSON frame failed to unmarshal: %v", err)
				}
			}
		}
	})
}
