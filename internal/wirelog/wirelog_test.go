package wirelog

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCaptureRequestResponse(t *testing.T) {
	log := New(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}`)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"jsonrpc":"2.0","method":"SendMessage"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sekrit")
	req.Header.Set("X-Trace", "t1")

	resp, err := log.Transport(nil).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	// Entry must not be complete until the body is consumed.
	if n := len(log.Snapshot()); n != 0 {
		t.Fatalf("entry committed before body read: %d", n)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if string(body) != `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}` {
		t.Fatalf("body passthrough broken: %q", body)
	}
	entries := log.Snapshot()
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Method != http.MethodPost || e.URL != srv.URL {
		t.Fatalf("bad request capture: %s %s", e.Method, e.URL)
	}
	if e.Status != http.StatusOK {
		t.Fatalf("bad status: %d", e.Status)
	}
	if got := string(e.ReqBody); got != `{"jsonrpc":"2.0","method":"SendMessage"}` {
		t.Fatalf("bad request body: %q", got)
	}
	if got := string(e.RespBody); got != `{"jsonrpc":"2.0","id":"1","result":{"ok":true}}` {
		t.Fatalf("bad response body: %q", got)
	}
	if auth := e.ReqHeaders.Get("Authorization"); auth != "REDACTED" {
		t.Fatalf("Authorization not redacted: %q", auth)
	}
	if tr := e.ReqHeaders.Get("X-Trace"); tr != "t1" {
		t.Fatalf("X-Trace lost: %q", tr)
	}
	if ct := e.RespHeaders.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("response content-type lost: %q", ct)
	}
	if e.Err != "" {
		t.Fatalf("unexpected transport error recorded: %q", e.Err)
	}
}

func TestCaptureTransportError(t *testing.T) {
	log := New(0)
	// Point at a closed port: request to a reserved address fails fast.
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Transport(nil).RoundTrip(req); err == nil {
		t.Fatal("expected transport error")
	}
	entries := log.Snapshot()
	if len(entries) != 1 || entries[0].Err == "" {
		t.Fatalf("error entry not captured: %+v", entries)
	}
	if entries[0].Status != 0 {
		t.Fatalf("status should be zero on error, got %d", entries[0].Status)
	}
}

func TestDisableStopsCapture(t *testing.T) {
	log := New(0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	log.SetEnabled(false)
	resp, err := log.Transport(nil).RoundTrip(mustPost(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n := len(log.Snapshot()); n != 0 {
		t.Fatalf("captured while disabled: %d entries", n)
	}

	log.SetEnabled(true)
	resp, err = log.Transport(nil).RoundTrip(mustPost(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n := len(log.Snapshot()); n != 1 {
		t.Fatalf("capture after re-enable: %d entries", n)
	}
}

func TestBodyCap(t *testing.T) {
	big := strings.Repeat("a", MaxBodyBytes+1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, big)
	}))
	defer srv.Close()

	log := New(0)
	resp, err := log.Transport(nil).RoundTrip(mustPostBody(t, srv.URL, big))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if n != int64(len(big)) {
		t.Fatalf("response truncated for caller: %d of %d", n, len(big))
	}

	e := log.Last()
	if e == nil {
		t.Fatal("no entry")
	}
	if len(e.RespBody) != MaxBodyBytes || !e.RespTrunc {
		t.Fatalf("response capture not capped: len=%d trunc=%v", len(e.RespBody), e.RespTrunc)
	}
	if len(e.ReqBody) != MaxBodyBytes || !e.ReqTrunc {
		t.Fatalf("request capture not capped: len=%d trunc=%v", len(e.ReqBody), e.ReqTrunc)
	}
}

func TestRingEviction(t *testing.T) {
	log := New(3)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.Query().Get("i"))
	}))
	defer srv.Close()

	rt := log.Transport(nil)
	for i := 0; i < 5; i++ {
		resp, err := rt.RoundTrip(mustPost(t, srv.URL+"?i="+string(rune('0'+i))))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	entries := log.Snapshot()
	if len(entries) != 3 {
		t.Fatalf("ring size: want 3, got %d", len(entries))
	}
	want := []string{"2", "3", "4"}
	for i, e := range entries {
		if string(e.RespBody) != want[i] {
			t.Fatalf("entry %d = %q, want %q", i, e.RespBody, want[i])
		}
	}
}

func TestStreamingBodyCommitsOnClose(t *testing.T) {
	chunks := []string{"data: one\n\n", "data: two\n\n"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			flusher.Flush()
		}
	}))
	defer srv.Close()

	log := New(0)
	resp, err := log.Transport(nil).RoundTrip(mustPost(t, srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != chunks[0]+chunks[1] {
		t.Fatalf("stream passthrough broken: %q", got)
	}
	e := log.Last()
	if e == nil || string(e.RespBody) != chunks[0]+chunks[1] {
		t.Fatalf("stream not captured: %+v", e)
	}
}

func mustPost(t *testing.T, url string) *http.Request {
	t.Helper()
	return mustPostBody(t, url, "{}")
}

func mustPostBody(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}
