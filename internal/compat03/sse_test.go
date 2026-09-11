package compat03

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// sseResponse wraps raw bytes as an SSE response for Frames.
func sseResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// collectFrames drains a Frames sequence, returning payloads and the
// first error (nil when the stream ended cleanly).
func collectFrames(ctx context.Context, body string) ([]string, error) {
	var got []string
	var firstErr error
	for frame, err := range Frames(ctx, sseResponse(body)) {
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		got = append(got, string(frame))
	}
	return got, firstErr
}

func TestFramesBasics(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "single line frames",
			in:   "data: one\n\ndata: two\n\n",
			want: []string{"one", "two"},
		},
		{
			name: "no space after colon",
			in:   "data:one\n\n",
			want: []string{"one"},
		},
		{
			name: "multi data lines joined with newline",
			in:   "data: first\ndata: second\ndata:third\n\n",
			want: []string{"first\nsecond\nthird"},
		},
		{
			name: "crlf endings",
			in:   "data: one\r\n\r\ndata: two\r\n\r\n",
			want: []string{"one", "two"},
		},
		{
			name: "comments and keep-alives ignored",
			in:   ": ok\n\n: ping\n\ndata: real\n\n\n\n",
			want: []string{"real"},
		},
		{
			name: "event id retry fields ignored",
			in:   "event: message\nid: 7\nretry: 1000\ndata: payload\n\n",
			want: []string{"payload"},
		},
		{
			name: "empty data value is a frame",
			in:   "data:\n\n",
			want: []string{""},
		},
		{
			name: "unterminated final line flushed at eof",
			in:   "data: tail",
			want: []string{"tail"},
		},
		{
			name: "stray lines without colon ignored",
			in:   "garbage\ndata: ok\n\n",
			want: []string{"ok"},
		},
		{
			name: "colon-only comment line ignored",
			in:   ":\ndata: ok\n\n",
			want: []string{"ok"},
		},
		{
			name: "empty stream",
			in:   "",
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := collectFrames(context.Background(), tt.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("frames = %q, want %q", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("frame %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestFramesJSONPayloadsUnmodified(t *testing.T) {
	// Frames must deliver JSON payloads byte-exact (no trimming beyond
	// the SSE field syntax) so JSON-RPC envelopes survive.
	payload := `{"jsonrpc":"2.0","id":1,"result":{"kind":"status-update","final":true}}`
	got, err := collectFrames(context.Background(), "data: "+payload+"\n\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0] != payload {
		t.Fatalf("frames = %q, want [%q]", got, payload)
	}
}

func TestFramesOverLineCapErrors(t *testing.T) {
	// A single data line over 1MB must error, not hang or panic.
	big := "data: " + strings.Repeat("x", maxLineBytes+1) + "\n\n"
	_, err := collectFrames(context.Background(), big)
	if err == nil || !strings.Contains(err.Error(), "line exceeds") {
		t.Fatalf("err = %v, want line-exceeds error", err)
	}
}

func TestFramesUnderLineCapAcrossBufferBoundary(t *testing.T) {
	// Lines larger than the 64KB bufio buffer (but under the cap) must
	// be reassembled correctly.
	payload := strings.Repeat("a", sseReadBuffer*3/2)
	body := "data: " + payload + "\n\ndata: short\n\n"
	got, err := collectFrames(context.Background(), body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0] != payload || got[1] != "short" {
		t.Fatalf("reassembly failed: %d frames, first len %d", len(got), len(got[0]))
	}
}

func TestFramesOverEventCapErrors(t *testing.T) {
	// Many data lines totaling over 1MB must error.
	var b strings.Builder
	chunk := strings.Repeat("d", 64*1024)
	for i := 0; i <= maxEventBytes/(64*1024); i++ {
		b.WriteString("data: " + chunk + "\n")
	}
	b.WriteString("\n")
	_, err := collectFrames(context.Background(), b.String())
	if err == nil || !strings.Contains(err.Error(), "event exceeds") {
		t.Fatalf("err = %v, want event-exceeds error", err)
	}
}

func TestFramesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled before first read
	resp := sseResponse("data: one\n\n")
	var frames, errs int
	for _, err := range Frames(ctx, resp) {
		if err != nil {
			errs++
		} else {
			frames++
		}
	}
	if frames != 0 || errs == 0 {
		t.Fatalf("frames = %d, errs = %d; want no frames and at least one error", frames, errs)
	}
}

func TestFramesNilBody(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusOK, Body: nil}
	var errs int
	for _, err := range Frames(context.Background(), resp) {
		if err != nil {
			errs++
		}
	}
	if errs != 1 {
		t.Fatalf("errs = %d, want 1", errs)
	}
}

func TestFramesTerminatesOnJunk(t *testing.T) {
	// Property: arbitrary bytes never panic and always terminate.
	junk := []string{
		"\x00\x01\x02\xff\xfe",
		strings.Repeat("\n", 1000),
		"data: \x00\x01\x02\n\n",
		"data",
		":",
		strings.Repeat("data:", 10000),
	}
	for i, j := range junk {
		done := make(chan struct{})
		go func(body string) {
			defer close(done)
			_, _ = collectFrames(context.Background(), body)
		}(j)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("junk case %d did not terminate", i)
		}
	}
}

// shortenIdleTimeout points the global SSE idle timeout at d for the
// duration of the test.
func shortenIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	restore := idleReadTimeout
	idleReadTimeout = d
	t.Cleanup(func() { idleReadTimeout = restore })
}

func TestFramesIdleWatchdogUnblocksSilentStream(t *testing.T) {
	// Issue #37: stdlib response bodies carry no read deadline, so a
	// stream that fell silent blocked readLine forever. The watchdog
	// must close the body and surface an idle error instead.
	shortenIdleTimeout(t, 100*time.Millisecond)

	pr, pw := io.Pipe()
	_ = pw // never written nor closed: reads block until the watchdog fires
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}
	done := make(chan error, 1)
	go func() {
		var firstErr error
		for _, err := range Frames(context.Background(), resp) {
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		done <- firstErr
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "idle") {
			t.Fatalf("err = %v, want idle timeout", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("silent stream was not unblocked by the idle watchdog")
	}
}

func TestFramesWatchdogResetOnProgress(t *testing.T) {
	// A stream that keeps producing frames well past the idle timeout
	// must never trip the watchdog.
	shortenIdleTimeout(t, 250*time.Millisecond)

	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		for i := 0; i < 8; i++ {
			fmt.Fprintf(pw, "data: %d\n\n", i)
			time.Sleep(50 * time.Millisecond)
		}
	}()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       pr,
	}
	var frames []string
	for frame, err := range Frames(context.Background(), resp) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		frames = append(frames, string(frame))
	}
	if len(frames) != 8 || frames[0] != "0" || frames[7] != "7" {
		t.Fatalf("frames = %q, want 0..7", frames)
	}
}
