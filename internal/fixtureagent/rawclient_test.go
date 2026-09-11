package fixtureagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// rawClient drives the fixture agent over raw JSON-RPC 2.0 posts and SSE
// reads, using only net/http and encoding/json. It deliberately avoids both
// the SDK client and internal/agent so the fixture is verified against the
// wire protocol itself, not against its own dependencies.
type rawClient struct {
	t      *testing.T
	base   string
	hc     *http.Client
	nextID int
}

func newRawClient(t *testing.T, base string) *rawClient {
	t.Helper()
	return &rawClient{t: t, base: base, hc: &http.Client{Timeout: 30 * time.Second}}
}

// startFixture serves the fixture behind httptest on an ephemeral port and
// returns a raw client pointed at it.
func startFixture(t *testing.T) (*rawClient, *httptest.Server) {
	t.Helper()
	ts, err := StartTest()
	if err != nil {
		t.Fatalf("StartTest: %v", err)
	}
	t.Cleanup(ts.Close)
	return newRawClient(t, ts.URL), ts
}

// --- JSON-RPC plumbing -------------------------------------------------

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResp struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcErr         `json:"error"`
}

// post issues one JSON-RPC request and returns the raw HTTP response.
func (c *rawClient) post(ctx context.Context, method string, params any) *http.Response {
	c.t.Helper()
	c.nextID++
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		c.t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/", bytes.NewReader(body))
	if err != nil {
		c.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", "1.0")
	resp, err := c.hc.Do(req)
	if err != nil {
		c.t.Fatalf("%s: post: %v", method, err)
	}
	return resp
}

// call performs a unary JSON-RPC request, failing the test on transport or
// protocol errors, and returns the result member.
func (c *rawClient) call(ctx context.Context, method string, params any) json.RawMessage {
	c.t.Helper()
	resp := c.post(ctx, method, params)
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		c.t.Fatalf("%s: content-type = %q, want application/json", method, ct)
	}
	var out rpcResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		c.t.Fatalf("%s: decode response: %v", method, err)
	}
	if out.Error != nil {
		c.t.Fatalf("%s: json-rpc error %d: %s", method, out.Error.Code, out.Error.Message)
	}
	return out.Result
}

// --- SendMessage / SendStreamingMessage ---------------------------------

// sendOpts customizes an outbound message; zero values select the defaults
// (a single text part, no task continuation, no configuration).
type sendOpts struct {
	taskID    string
	contextID string
	config    map[string]any
	parts     []any
	metadata  map[string]any
}

func (c *rawClient) sendParams(text string, o sendOpts) map[string]any {
	c.t.Helper()
	parts := o.parts
	if parts == nil {
		parts = []any{map[string]any{"text": text}}
	}
	msg := map[string]any{
		"messageId": fmt.Sprintf("fixture-test-%d", c.nextID+1),
		"role":      "ROLE_USER",
		"parts":     parts,
	}
	if o.taskID != "" {
		msg["taskId"] = o.taskID
	}
	if o.contextID != "" {
		msg["contextId"] = o.contextID
	}
	if o.metadata != nil {
		msg["metadata"] = o.metadata
	}
	params := map[string]any{"message": msg}
	if o.config != nil {
		params["configuration"] = o.config
	}
	return params
}

// sendMessage posts a unary SendMessage and returns its result.
func (c *rawClient) sendMessage(ctx context.Context, text string, o sendOpts) json.RawMessage {
	c.t.Helper()
	return c.call(ctx, "SendMessage", c.sendParams(text, o))
}

// openStream posts SendStreamingMessage and returns a buffered reader over
// the SSE body.
func (c *rawClient) openStream(ctx context.Context, text string, o sendOpts) (*http.Response, *bufio.Reader) {
	c.t.Helper()
	resp := c.post(ctx, "SendStreamingMessage", c.sendParams(text, o))
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		_ = resp.Body.Close()
		c.t.Fatalf("SendStreamingMessage: content-type = %q, want text/event-stream", ct)
	}
	return resp, bufio.NewReader(resp.Body)
}

// readSSEFrame reads one SSE event (the joined data: lines) until a blank
// line; ok is false at clean EOF.
func readSSEFrame(br *bufio.Reader) ([]byte, bool) {
	var data []string
	for {
		line, err := br.ReadString('\n')
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case trimmed == "":
			if len(data) > 0 {
				return []byte(strings.Join(data, "\n")), true
			}
			if err != nil {
				return nil, false
			}
		case strings.HasPrefix(trimmed, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
		default:
			// id:, comments and unknown fields are ignored.
		}
		if err != nil {
			if err == io.EOF && len(data) > 0 {
				return []byte(strings.Join(data, "\n")), true
			}
			return nil, false
		}
	}
}

// readAllSSEFrames drains the stream to EOF.
func readAllSSEFrames(br *bufio.Reader) [][]byte {
	var frames [][]byte
	for {
		frame, ok := readSSEFrame(br)
		if !ok {
			return frames
		}
		frames = append(frames, frame)
	}
}

// streamEvents posts a streaming send and drains it, asserting that every
// frame is a successful JSON-RPC response carrying one stream event.
func (c *rawClient) streamEvents(ctx context.Context, text string, o sendOpts) []wireEvent {
	c.t.Helper()
	resp, br := c.openStream(ctx, text, o)
	defer func() { _ = resp.Body.Close() }()
	var events []wireEvent
	for _, frame := range readAllSSEFrames(br) {
		var out rpcResp
		if err := json.Unmarshal(frame, &out); err != nil {
			c.t.Fatalf("decode sse frame %s: %v", frame, err)
		}
		if out.Error != nil {
			c.t.Fatalf("sse frame error %d: %s", out.Error.Code, out.Error.Message)
		}
		events = append(events, decodeEvent(out.Result))
	}
	return events
}

// --- Wire shapes ---------------------------------------------------------

// wireEvent is the StreamResponse oneof: exactly one member is set.
type wireEvent struct {
	Task           *wireTask         `json:"task"`
	StatusUpdate   *wireStatusUpdate `json:"statusUpdate"`
	ArtifactUpdate *wireArtifactUpd  `json:"artifactUpdate"`
	Message        *wireMessage      `json:"message"`
}

func (e wireEvent) kind() string {
	switch {
	case e.Task != nil:
		return "task"
	case e.StatusUpdate != nil:
		return "statusUpdate"
	case e.ArtifactUpdate != nil:
		return "artifactUpdate"
	case e.Message != nil:
		return "message"
	default:
		return "empty"
	}
}

type wireTask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    wireStatusBody `json:"status"`
	Artifacts []wireArtifact `json:"artifacts"`
}

type wireStatusUpdate struct {
	TaskID string         `json:"taskId"`
	Status wireStatusBody `json:"status"`
}

type wireStatusBody struct {
	State   string       `json:"state"`
	Message *wireMessage `json:"message"`
}

type wireMessage struct {
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId"`
	Parts     []wirePart `json:"parts"`
}

type wirePart struct {
	Text      *string         `json:"text"`
	Data      json.RawMessage `json:"data"`
	MediaType string          `json:"mediaType"`
	Metadata  map[string]any  `json:"metadata"`
}

type wireArtifactUpd struct {
	TaskID    string       `json:"taskId"`
	Append    bool         `json:"append"`
	LastChunk bool         `json:"lastChunk"`
	Artifact  wireArtifact `json:"artifact"`
}

type wireArtifact struct {
	ID    string     `json:"artifactId"`
	Parts []wirePart `json:"parts"`
}

// text joins the text parts of a message.
func (m *wireMessage) text() string {
	if m == nil {
		return ""
	}
	var texts []string
	for _, p := range m.Parts {
		if p.Text != nil {
			texts = append(texts, *p.Text)
		}
	}
	return strings.Join(texts, " ")
}

func (a *wireArtifact) text() string {
	if a == nil {
		return ""
	}
	var texts []string
	for _, p := range a.Parts {
		if p.Text != nil {
			texts = append(texts, *p.Text)
		}
	}
	return strings.Join(texts, "")
}

// decodeEvent unmarshals a StreamResponse result.
func decodeEvent(raw json.RawMessage) wireEvent {
	var ev wireEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return wireEvent{}
	}
	return ev
}

// expectTaskResult decodes a SendMessage result that must carry a task.
func (c *rawClient) expectTaskResult(raw json.RawMessage) *wireTask {
	c.t.Helper()
	ev := decodeEvent(raw)
	if ev.Task == nil {
		c.t.Fatalf("SendMessage result kind = %s, want task (raw %s)", ev.kind(), raw)
	}
	return ev.Task
}

// expectMessageResult decodes a SendMessage result that must carry a
// terminal agent message.
func (c *rawClient) expectMessageResult(raw json.RawMessage) *wireMessage {
	c.t.Helper()
	ev := decodeEvent(raw)
	if ev.Message == nil {
		c.t.Fatalf("SendMessage result kind = %s, want message (raw %s)", ev.kind(), raw)
	}
	return ev.Message
}

// getTask fetches a task by id via GetTask. GetTask (and CancelTask)
// return the bare task object, unlike SendMessage whose result wraps the
// event in a StreamResponse oneof.
func (c *rawClient) getTask(ctx context.Context, id string) *wireTask {
	c.t.Helper()
	raw := c.call(ctx, "GetTask", map[string]any{"id": id})
	var task wireTask
	if err := json.Unmarshal(raw, &task); err != nil || task.ID == "" {
		c.t.Fatalf("GetTask result is not a task: %v (raw %s)", err, raw)
	}
	return &task
}

// waitTaskState polls GetTask until the task reaches want or the deadline.
func (c *rawClient) waitTaskState(ctx context.Context, id, want string, timeout time.Duration) *wireTask {
	c.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		task := c.getTask(ctx, id)
		if task.Status.State == want {
			return task
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("task %s never reached %s, last state %s", id, want, task.Status.State)
		}
		select {
		case <-ctx.Done():
			c.t.Fatalf("context done waiting for %s: %v", want, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
