package rpcconsole

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedReq captures what one POST looked like to the server.
type recordedReq struct {
	method      string
	id          float64
	params      string // raw params member, "" when absent
	contentType string
	accept      string
	version     string // A2A-Version header ("" when absent)
	auth        string
	body        string
}

// recordingServer answers every POST with respond and records the request.
func recordingServer(t *testing.T, respond func(w http.ResponseWriter, r *http.Request, req recordedReq)) (*httptest.Server, *[]recordedReq) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recordedReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var env struct {
			ID     float64         `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &env)
		req := recordedReq{
			method:      env.Method,
			id:          env.ID,
			params:      string(env.Params),
			contentType: r.Header.Get("Content-Type"),
			accept:      r.Header.Get("Accept"),
			version:     r.Header.Get("A2A-Version"),
			auth:        r.Header.Get("Authorization"),
			body:        string(body),
		}
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		respond(w, r, req)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestCallJSONResponse(t *testing.T) {
	srv, reqs := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, req recordedReq) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"id":"t-1","kind":"task"}}`, int64(req.id))
	})
	c := New(srv.URL, srv.Client())

	resp, frames, err := c.Call(context.Background(), "GetTask", json.RawMessage(`{"id":"t-1"}`), "1.0", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(frames) != 0 {
		t.Errorf("frames = %v, want none for a JSON answer", frames)
	}
	var env struct {
		Result struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &env); err != nil || env.Result.ID != "t-1" {
		t.Fatalf("resp = %s (%v)", resp, err)
	}

	got := *reqs
	if len(got) != 1 {
		t.Fatalf("recorded %d requests", len(got))
	}
	r := got[0]
	if r.method != "GetTask" || r.params != `{"id":"t-1"}` {
		t.Errorf("method/params = %q / %s", r.method, r.params)
	}
	if r.contentType != "application/json" {
		t.Errorf("Content-Type = %q", r.contentType)
	}
	if !strings.Contains(r.accept, "text/event-stream") {
		t.Errorf("Accept = %q, must offer event-stream", r.accept)
	}
	if r.version != "1.0" {
		t.Errorf("A2A-Version = %q, want 1.0", r.version)
	}
	if !strings.Contains(r.body, `"jsonrpc":"2.0"`) || !strings.Contains(r.body, `"method":"GetTask"`) {
		t.Errorf("envelope = %s", r.body)
	}
}

func TestCallIDsIncrement(t *testing.T) {
	srv, reqs := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":0,"result":null}`)
	})
	c := New(srv.URL, srv.Client())
	for range 3 {
		if _, _, err := c.Call(context.Background(), "ListTasks", nil, "1.0", nil); err != nil {
			t.Fatalf("Call: %v", err)
		}
	}
	for i, r := range *reqs {
		if r.id != float64(i+1) {
			t.Errorf("request %d id = %v, want %d", i, r.id, i+1)
		}
	}
	// A nil params member must be omitted entirely.
	if strings.Contains((*reqs)[0].body, "params") {
		t.Errorf("nil params must be omitted: %s", (*reqs)[0].body)
	}
}

func TestCallOmitsNullParams(t *testing.T) {
	srv, reqs := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":0,"result":null}`)
	})
	c := New(srv.URL, srv.Client())
	if _, _, err := c.Call(context.Background(), "GetExtendedAgentCard", json.RawMessage("null"), "", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if strings.Contains((*reqs)[0].body, "params") {
		t.Errorf("null params must be omitted: %s", (*reqs)[0].body)
	}
}

func TestCallSSEResponse(t *testing.T) {
	sse := "data: {\"result\":{\"a\":1}}\n\n" +
		": keep-alive comment\n\n" +
		"data: {\"result\":{\"a\":2},\n" +
		"data:  \"more\":\"joined\"}\n\n"
	srv, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, sse)
	})
	c := New(srv.URL, srv.Client())

	resp, frames, err := c.Call(context.Background(), "SubscribeToTask", json.RawMessage(`{"id":"t-1"}`), "1.0", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if resp != nil {
		t.Errorf("resp = %s, want nil for SSE", resp)
	}
	// The SSE reader strips the single space after "data:" per event line;
	// the second space of the double-spaced continuation line survives.
	want := []string{`{"result":{"a":1}}`, "{\"result\":{\"a\":2},\n \"more\":\"joined\"}"}
	if len(frames) != len(want) {
		t.Fatalf("frames = %#v", frames)
	}
	for i := range want {
		if frames[i] != want[i] {
			t.Errorf("frame[%d] = %q, want %q", i, frames[i], want[i])
		}
	}

	// The exchange is recorded as an SSE one.
	h := c.History()
	if len(h) != 1 || !h[0].SSE || h[0].Err != "" || len(h[0].Frames) != 2 {
		t.Fatalf("history = %+v", h)
	}
}

func TestCallErrorResponsesAreData(t *testing.T) {
	t.Run("json-rpc error body", func(t *testing.T) {
		srv, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"TaskNotFound: t-x"}}`)
		})
		c := New(srv.URL, srv.Client())
		resp, _, err := c.Call(context.Background(), "GetTask", json.RawMessage(`{"id":"t-x"}`), "1.0", nil)
		if err != nil {
			t.Fatalf("protocol errors are data, not Go errors: %v", err)
		}
		if !strings.Contains(string(resp), "TaskNotFound") {
			t.Errorf("resp = %s", resp)
		}
	})

	t.Run("http 500 with body", func(t *testing.T) {
		srv, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom")
		})
		c := New(srv.URL, srv.Client())
		resp, _, err := c.Call(context.Background(), "SendMessage", nil, "1.0", nil)
		if err != nil {
			t.Fatalf("http status is data, not a Go error: %v", err)
		}
		if string(resp) != "boom" {
			t.Errorf("resp = %q", resp)
		}
		if h := c.History(); len(h) != 1 || h[0].Status != 500 {
			t.Fatalf("history = %+v", h)
		}
	})
}

func TestCallTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := New(srv.URL, srv.Client())
	srv.Close() // everything after this fails at the transport layer

	_, _, err := c.Call(context.Background(), "GetTask", nil, "1.0", nil)
	if err == nil {
		t.Fatal("expected transport error")
	}
	h := c.History()
	if len(h) != 1 || h[0].Err == "" || h[0].Status != 0 {
		t.Fatalf("history = %+v", h)
	}
	if h[0].Method != "GetTask" {
		t.Errorf("method = %q", h[0].Method)
	}
}

func TestCallHeaders(t *testing.T) {
	srv, reqs := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	c := New(srv.URL, srv.Client())

	// 0.3 dialect: no A2A-Version header at all.
	if _, _, err := c.Call(context.Background(), "message/send", nil, "", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if v := (*reqs)[0].version; v != "" {
		t.Errorf("0.3 call sent A2A-Version %q", v)
	}

	// Stored-credentials header override.
	auth := map[string]string{"Authorization": "Bearer tok-1", "X-API-Key": "k"}
	if _, _, err := c.Call(context.Background(), "message/send", nil, "", auth); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if v := (*reqs)[1].auth; v != "Bearer tok-1" {
		t.Errorf("Authorization = %q", v)
	}
}

func TestCallFrameCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := range MaxFrames + 20 {
			fmt.Fprintf(w, "data: {\"n\":%d}\n\n", i)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, srv.Client())

	_, frames, err := c.Call(context.Background(), "SubscribeToTask", nil, "1.0", nil)
	if !errors.Is(err, ErrTooManyFrames) {
		t.Fatalf("err = %v, want ErrTooManyFrames", err)
	}
	if len(frames) != MaxFrames {
		t.Errorf("frames = %d, want the capped %d", len(frames), MaxFrames)
	}
}

func TestCallSizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := strings.Repeat("x", 32<<10)
		for range 64 { // 2MB of frames total
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, srv.Client())

	_, frames, err := c.Call(context.Background(), "SubscribeToTask", nil, "1.0", nil)
	if !errors.Is(err, ErrStreamTooLarge) {
		t.Fatalf("err = %v, want ErrStreamTooLarge", err)
	}
	total := 0
	for _, f := range frames {
		total += len(f)
	}
	if total > MaxStreamBytes+32<<10 {
		t.Errorf("collected %d bytes, cap not applied", total)
	}
}

func TestCallBodySizeCap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("a", MaxResponseBytes+512))
	}))
	defer srv.Close()
	c := New(srv.URL, srv.Client())

	resp, _, err := c.Call(context.Background(), "ListTasks", nil, "1.0", nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("err = %v, want ErrResponseTooLarge", err)
	}
	if len(resp) != MaxResponseBytes {
		t.Errorf("resp = %d bytes, want the capped %d", len(resp), MaxResponseBytes)
	}
}

func TestCallTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c := New(srv.URL, srv.Client())

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, _, err := c.Call(ctx, "GetTask", nil, "1.0", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Call took %v; the per-call bound did not apply", elapsed)
	}
}

func TestHistoryRing(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	c := New(srv.URL, srv.Client())
	for i := range HistorySize + 10 {
		m := fmt.Sprintf("method/%d", i)
		if _, _, err := c.Call(context.Background(), m, nil, "", nil); err != nil {
			t.Fatalf("Call %s: %v", m, err)
		}
	}
	h := c.History()
	if len(h) != HistorySize {
		t.Fatalf("history = %d entries, want %d", len(h), HistorySize)
	}
	if h[0].Method != fmt.Sprintf("method/%d", 10) {
		t.Errorf("oldest = %q, want method/10", h[0].Method)
	}
	if h[len(h)-1].Method != fmt.Sprintf("method/%d", HistorySize+9) {
		t.Errorf("newest = %q", h[len(h)-1].Method)
	}
	// The ring must not alias the caller's copy.
	h[0].Method = "mutated"
	if c.History()[0].Method == "mutated" {
		t.Error("History() returned an aliased slice")
	}
}

func TestCallConcurrentUse(t *testing.T) {
	srv, _ := recordingServer(t, func(w http.ResponseWriter, _ *http.Request, _ recordedReq) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	})
	c := New(srv.URL, srv.Client())
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := c.Call(context.Background(), fmt.Sprintf("m%d", i), nil, "1.0", nil)
			if err != nil {
				t.Errorf("Call: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(c.History()) != 8 {
		t.Fatalf("history = %d, want 8", len(c.History()))
	}
}
