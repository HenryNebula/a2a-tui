package pushsrv

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

func TestNewToken(t *testing.T) {
	tok, err := NewToken()
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	if len(tok) != TokenHexBytes*2 {
		t.Fatalf("token = %q (%d chars), want %d hex chars", tok, len(tok), TokenHexBytes*2)
	}
	tok2, _ := NewToken()
	if tok == tok2 {
		t.Fatal("tokens should be unique")
	}
}

func TestDecodeV10Body(t *testing.T) {
	body := []byte(`{"statusUpdate": {
		"taskId": "t-1", "contextId": "c-1",
		"status": {"state": "TASK_STATE_WORKING",
			"message": {"role": "agent", "parts": [{"kind": "text", "text": "pushed"}]}}
	}}`)
	ev, err := DecodeEvent(body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	upd, ok := ev.(*a2a.TaskStatusUpdateEvent)
	if !ok {
		t.Fatalf("event = %T, want *a2a.TaskStatusUpdateEvent", ev)
	}
	if string(upd.TaskID) != "t-1" || upd.ContextID != "c-1" {
		t.Fatalf("ids = %q/%q", upd.TaskID, upd.ContextID)
	}
	if upd.Status.State != a2a.TaskStateWorking {
		t.Fatalf("state = %v", upd.Status.State)
	}
	if upd.Status.Message == nil || upd.Status.Message.Parts[0].Text() != "pushed" {
		t.Fatalf("status message = %+v", upd.Status.Message)
	}
}

func TestDecodeV10TaskBody(t *testing.T) {
	body := []byte(`{"task": {"id": "t-9", "contextId": "c-9",
		"status": {"state": "TASK_STATE_COMPLETED"},
		"artifacts": [{"artifactId": "a-1", "parts": [{"kind": "text", "text": "result"}]}]}}`)
	ev, err := DecodeEvent(body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	task, ok := ev.(*a2a.Task)
	if !ok {
		t.Fatalf("event = %T, want *a2a.Task", ev)
	}
	if task.ID != "t-9" || task.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Artifacts) != 1 || task.Artifacts[0].Parts[0].Text() != "result" {
		t.Fatalf("artifacts = %+v", task.Artifacts)
	}
}

func TestDecodeV03Body(t *testing.T) {
	body := []byte(`{"kind": "status-update", "taskId": "t-2", "contextId": "c-2",
		"final": true,
		"status": {"state": "completed",
			"message": {"messageId": "m-1", "role": "agent",
				"parts": [{"kind": "text", "text": "legacy push"}]}}}`)
	ev, err := DecodeEvent(body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	upd, ok := ev.(*a2a.TaskStatusUpdateEvent)
	if !ok {
		t.Fatalf("event = %T, want *a2a.TaskStatusUpdateEvent", ev)
	}
	if string(upd.TaskID) != "t-2" || upd.Status.State != a2a.TaskStateCompleted {
		t.Fatalf("update = %+v", upd)
	}
	if upd.Status.Message == nil || upd.Status.Message.Parts[0].Text() != "legacy push" {
		t.Fatalf("message = %+v", upd.Status.Message)
	}
}

func TestDecodeV03KindlessTask(t *testing.T) {
	// Early 0.3 servers sent bare tasks with no kind member.
	body := []byte(`{"id": "t-3", "contextId": "c-3",
		"status": {"state": "failed", "message": null}}`)
	ev, err := DecodeEvent(body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	task, ok := ev.(*a2a.Task)
	if !ok {
		t.Fatalf("event = %T, want *a2a.Task", ev)
	}
	if task.ID != "t-3" || task.Status.State != a2a.TaskStateFailed {
		t.Fatalf("task = %+v", task)
	}
}

func TestDecodeV03ArtifactUpdate(t *testing.T) {
	body := []byte(`{"kind": "artifact-update", "taskId": "t-4",
		"artifact": {"artifactId": "a-9", "name": "report.md",
			"parts": [{"kind": "data", "data": {"rows": 3}}]},
		"append": true, "lastChunk": true}`)
	ev, err := DecodeEvent(body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}
	upd, ok := ev.(*a2a.TaskArtifactUpdateEvent)
	if !ok {
		t.Fatalf("event = %T, want *a2a.TaskArtifactUpdateEvent", ev)
	}
	if string(upd.TaskID) != "t-4" || string(upd.Artifact.ID) != "a-9" || !upd.LastChunk {
		t.Fatalf("update = %+v", upd)
	}
	if upd.Artifact.Parts[0].Data() == nil {
		t.Fatalf("data part lost: %+v", upd.Artifact.Parts[0])
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for name, body := range map[string]string{
		"empty":   "",
		"null":    "null",
		"number":  "42",
		"unknown": `{"kind": "spam"}`,
		"neither": `{"stuff": 1}`,
	} {
		if _, err := DecodeEvent([]byte(body)); err == nil {
			t.Errorf("%s: DecodeEvent(%q) should fail", name, body)
		}
	}
}

// startTestServer boots a listener and returns it plus its base URL.
func startTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	srv, err := Start("", "test-token-0123456789abcdef", "", func(a2a.Event) {})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	return srv, "http://" + srv.Addr()
}

func TestServerTokenRequired(t *testing.T) {
	_, base := startTestServer(t)
	body := []byte(`{"message": {"role": "agent", "parts": []}}`)

	for name, build := range map[string]func() *http.Request{
		"no token": func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, base+Path, bytes.NewReader(body))
			return req
		},
		"wrong token": func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, base+Path, bytes.NewReader(body))
			req.Header.Set(notificationTokenHeader, "nope")
			return req
		},
		"wrong bearer": func() *http.Request {
			req, _ := http.NewRequest(http.MethodPost, base+Path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer nope")
			return req
		},
	} {
		resp, err := http.DefaultClient.Do(build())
		if err != nil {
			t.Fatalf("%s: POST: %v", name, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, resp.StatusCode)
		}
	}
}

func TestServerDeliversBothTokenSpellings(t *testing.T) {
	var mu sync.Mutex
	var got []a2a.Event
	srv, err := Start("", "tok", "", func(ev a2a.Event) {
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	base := "http://" + srv.Addr()

	body := []byte(`{"statusUpdate": {"taskId": "t-ok",
		"status": {"state": "TASK_STATE_WORKING"}}}`)
	for name, hdr := range map[string][2]string{
		"notification header": {notificationTokenHeader, "tok"},
		"bearer":              {"Authorization", "Bearer tok"},
	} {
		req, _ := http.NewRequest(http.MethodPost, base+Path, bytes.NewReader(body))
		req.Header.Set(hdr[0], hdr[1])
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: POST: %v", name, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: status = %d, want 204", name, resp.StatusCode)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("delivered %d events, want 2", len(got))
	}
	if upd, ok := got[0].(*a2a.TaskStatusUpdateEvent); !ok || string(upd.TaskID) != "t-ok" {
		t.Fatalf("first event = %#v", got[0])
	}
}

func TestServerBadBody(t *testing.T) {
	_, base := startTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, base+Path, strings.NewReader("not json"))
	req.Header.Set(notificationTokenHeader, "test-token-0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServerBodyCap(t *testing.T) {
	_, base := startTestServer(t)
	huge := bytes.Repeat([]byte("a"), MaxBodyBytes+1024)
	req, _ := http.NewRequest(http.MethodPost, base+Path, bytes.NewReader(huge))
	req.Header.Set(notificationTokenHeader, "test-token-0123456789abcdef")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
}

func TestServerMethodAndPath(t *testing.T) {
	_, base := startTestServer(t)

	get, _ := http.NewRequest(http.MethodGet, base+Path, nil)
	resp, err := http.DefaultClient.Do(get)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/elsewhere", strings.NewReader("{}"))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST wrong path: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("wrong path status = %d, want 404", resp.StatusCode)
	}
}

func TestServerShutdown(t *testing.T) {
	srv, err := Start("", "tok", "", func(a2a.Event) {})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	base := "http://" + srv.Addr()
	advertised := srv.URL()
	if !strings.HasSuffix(advertised, Path) {
		t.Fatalf("URL = %q, want suffix %s", advertised, Path)
	}

	srv.Shutdown()
	srv.Shutdown() // idempotent

	req, _ := http.NewRequest(http.MethodPost, base+Path, strings.NewReader("{}"))
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("POST after shutdown should fail, got status %d", resp.StatusCode)
	}
}

func TestStartRejectsBadArgs(t *testing.T) {
	if _, err := Start("", "", "", func(a2a.Event) {}); err == nil {
		t.Error("empty token should be rejected")
	}
	if _, err := Start("", "tok", "", nil); err == nil {
		t.Error("nil deliver should be rejected")
	}
	// A custom advertised URL wins over the derived one.
	srv, err := Start("", "tok", "https://tunnel.example.com/hook", func(a2a.Event) {})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Shutdown()
	if want := "https://tunnel.example.com/hook"; srv.URL() != want {
		t.Fatalf("URL = %q, want %q", srv.URL(), want)
	}
}

// TestDecodeAllV10Variants walks each StreamResponse member once.
func TestDecodeAllV10Variants(t *testing.T) {
	const messageBody = `{"message": {"role": "agent", "parts": [{"kind": "text", "text": "hi"}]}}`
	for _, body := range []string{
		messageBody,
		`{"task": {"id": "t", "status": {"state": "TASK_STATE_SUBMITTED"}}}`,
		`{"artifactUpdate": {"taskId": "t", "artifact": {"artifactId": "a", "parts": []}}}`,
	} {
		ev, err := DecodeEvent([]byte(body))
		if err != nil {
			t.Errorf("DecodeEvent(%s): %v", body, err)
			continue
		}
		if ev == nil {
			t.Errorf("DecodeEvent(%s) = nil event", body)
		}
	}
	// Sanity for the message variant's shape.
	ev, err := DecodeEvent([]byte(messageBody))
	if err != nil || ev == nil {
		t.Fatalf("message decode: %v %#v", err, ev)
	}
	if msg, ok := ev.(*a2a.Message); !ok || msg.Parts[0].Text() != "hi" {
		t.Fatalf("message = %#v", ev)
	}
}
