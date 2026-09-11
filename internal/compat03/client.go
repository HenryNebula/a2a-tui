package compat03

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/google/uuid"

	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// maxEventsPerStream bounds one streaming session so a hostile or buggy
// agent cannot spin forever.
const maxEventsPerStream = 10000

// Client is a connection to an A2A 0.3 agent: JSON-RPC 2.0 POSTs to the
// card url with SSE streaming for message/stream and tasks/resubscribe.
// No A2A-Version header is sent (it does not exist in 0.3).
//
// The method surface mirrors connv1.Conn and speaks SDK a2a types; the
// internal/agent package adapts it to agent.AgentConn.
type Client struct {
	base string
	http *http.Client
	card *CardV03
}

// New builds a Client posting to baseURL (the card url the resolver
// pinned — it may differ from the host the card was fetched from). card
// may be nil (the connection then assumes no optional capability).
//
// httpClient may be nil; when provided its Timeout is cleared (SSE
// streams outlive short client timeouts — cancellation is
// context-driven) and its Transport is wrapped by log's capturing round
// tripper when log is non-nil, exactly like the v1 connection.
func New(_ context.Context, baseURL string, card *CardV03, httpClient *http.Client, log *wirelog.Logger) (*Client, error) {
	if !isHTTPURL(baseURL) {
		return nil, fmt.Errorf("compat03: agent url %q is not a valid http(s) URL", baseURL)
	}
	hc := &http.Client{}
	if httpClient != nil {
		*hc = *httpClient
	}
	hc.Timeout = 0
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if log != nil {
		transport = log.Transport(transport)
	}
	hc.Transport = transport
	return &Client{base: baseURL, http: hc, card: card}, nil
}

// BaseURL is the JSON-RPC POST target.
func (c *Client) BaseURL() string { return c.base }

// WireVersion reports the wire protocol flavor ("0.3").
func (c *Client) WireVersion() string { return "0.3" }

// Card returns nil: 0.3 cards have no 1.x representation. Use CardV03
// for the raw legacy card.
func (c *Client) Card() *a2a.AgentCard { return nil }

// CardV03 returns the legacy card the connection was built from, or nil.
func (c *Client) CardV03() *CardV03 { return c.card }

// Capabilities reports what the card declared (zero values when the
// connection was built without a card).
func (c *Client) Capabilities() CapabilitiesV03 {
	if c.card == nil {
		return CapabilitiesV03{}
	}
	return c.card.Capabilities
}

// Destroy releases connection resources. The 0.3 client holds no
// pooled state, so this is a no-op; it exists for interface parity and
// is idempotent.
func (c *Client) Destroy() error { return nil }

// SendMessage implements the message/send JSON-RPC method (blocking).
// The 0.3 result is the bare Task or Message object.
func (c *Client) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	params, err := sendParamsFromRequest(req, newID)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, methodSend, params)
	if err != nil {
		return nil, err
	}
	return c.sendResult(resp)
}

// SendStreamingMessage implements the message/stream JSON-RPC method
// (SSE). The sequence ends after the final status-update (final:true),
// a terminal state, an error, or consumer break — whichever comes first.
func (c *Client) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return c.stream(ctx, methodStream, func() (any, error) {
		return sendParamsFromRequest(req, newID)
	})
}

// SubscribeToTask implements tasks/resubscribe (SSE) — the 0.3 analogue
// of the 1.x SubscribeToTask method.
func (c *Client) SubscribeToTask(ctx context.Context, taskID string) iter.Seq2[a2a.Event, error] {
	return c.stream(ctx, methodResubscribe, func() (any, error) {
		return wireTaskParams{ID: taskID}, nil
	})
}

// GetTask implements tasks/get. historyLength may be nil.
func (c *Client) GetTask(ctx context.Context, taskID string, historyLength *int) (*a2a.Task, error) {
	resp, err := c.post(ctx, methodGetTask, wireTaskParams{ID: taskID, HistoryLen: historyLength})
	if err != nil {
		return nil, err
	}
	var w wireTask
	if err := decodeResult(resp, &w); err != nil {
		return nil, err
	}
	return taskFromWire(&w), nil
}

// ListTasks is not supported by the 0.3 protocol.
func (c *Client) ListTasks(ctx context.Context) (*a2a.ListTasksResponse, error) {
	return nil, unsupported("tasks/list")
}

// CancelTask implements tasks/cancel.
func (c *Client) CancelTask(ctx context.Context, taskID string) (*a2a.Task, error) {
	resp, err := c.post(ctx, methodCancelTask, wireTaskParams{ID: taskID})
	if err != nil {
		return nil, err
	}
	var w wireTask
	if err := decodeResult(resp, &w); err != nil {
		return nil, err
	}
	return taskFromWire(&w), nil
}

// CreateTaskPushConfig implements tasks/pushNotificationConfig/set.
func (c *Client) CreateTaskPushConfig(ctx context.Context, cfg *a2a.PushConfig) (*a2a.PushConfig, error) {
	if cfg == nil {
		return nil, errors.New("compat03: nil push config")
	}
	resp, err := c.post(ctx, methodPushSet, pushToWire(cfg))
	if err != nil {
		return nil, err
	}
	var w wireTaskPushConfig
	if err := decodeResult(resp, &w); err != nil {
		return nil, err
	}
	return pushFromWire(w), nil
}

// GetTaskPushConfig implements tasks/pushNotificationConfig/get. It has
// no 1.x counterpart on AgentConn but completes the 0.3 surface.
func (c *Client) GetTaskPushConfig(ctx context.Context, taskID, configID string) (*a2a.PushConfig, error) {
	resp, err := c.post(ctx, methodPushGet, wireTaskParams{ID: taskID, ConfigID: configID})
	if err != nil {
		return nil, err
	}
	var w wireTaskPushConfig
	if err := decodeResult(resp, &w); err != nil {
		return nil, err
	}
	return pushFromWire(w), nil
}

// ListTaskPushConfigs implements tasks/pushNotificationConfig/list.
func (c *Client) ListTaskPushConfigs(ctx context.Context, taskID string) ([]*a2a.PushConfig, error) {
	resp, err := c.post(ctx, methodPushList, wireTaskParams{ID: taskID})
	if err != nil {
		return nil, err
	}
	var configs []wireTaskPushConfig
	if err := decodeResult(resp, &configs); err != nil {
		return nil, err
	}
	if len(configs) > maxArtifactsPerTask {
		configs = configs[:maxArtifactsPerTask]
	}
	out := make([]*a2a.PushConfig, 0, len(configs))
	for _, w := range configs {
		out = append(out, pushFromWire(w))
	}
	return out, nil
}

// DeleteTaskPushConfig implements tasks/pushNotificationConfig/delete.
// The 0.3 result is null on success; any success envelope is accepted.
func (c *Client) DeleteTaskPushConfig(ctx context.Context, taskID, configID string) error {
	_, err := c.post(ctx, methodPushDelete, wireTaskParams{ID: taskID, ConfigID: configID})
	return err
}

// GetExtendedAgentCard is not supported by the 0.3 protocol.
func (c *Client) GetExtendedAgentCard(ctx context.Context) (*a2a.AgentCard, error) {
	return nil, unsupported("GetExtendedAgentCard")
}

// unsupported wraps the SDK sentinel so callers see a recognized,
// display-friendly "not available on this protocol version" error.
func unsupported(what string) error {
	return fmt.Errorf("compat03: %s is not supported by A2A 0.3: %w", what, a2a.ErrUnsupportedOperation)
}

// newID mints a JSON-RPC / message identifier.
func newID() string { return uuid.NewString() }

// isHTTPURL reports whether s parses as an absolute http(s) URL.
func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// newRequest builds the JSON-RPC POST request. The 0.3 protocol sends
// no A2A-Version header (see client_test.go's assertion).
func (c *Client) newRequest(ctx context.Context, method string, params any, accept string) (*http.Request, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: newID(), Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("compat03: encode %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("compat03: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", jsonMIME)
	req.Header.Set("Accept", accept)
	return req, nil
}

// post performs one blocking JSON-RPC call and validates the envelope.
func (c *Client) post(ctx context.Context, method string, params any) (*rpcResponse, error) {
	req, err := c.newRequest(ctx, method, params, jsonMIME)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(ctx, method, err)
	}
	defer resp.Body.Close()
	body, err := readCapped(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("compat03: read %s response: %w", method, err)
	}
	if resp.StatusCode != http.StatusOK {
		if rpcErr := parseErrorBody(body); rpcErr != nil {
			return nil, rpcErr
		}
		return nil, fmt.Errorf("compat03: %s failed: %s: %s", method, resp.Status, snippet(body))
	}
	var env rpcResponse
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("compat03: decode %s response: %w: %s", method, err, snippet(body))
	}
	if env.Error != nil {
		return nil, a2aError(env.Error)
	}
	return &env, nil
}

// sendResult decodes the message/send result: the bare Task or Message
// object, discriminated by kind (with shape sniffing for servers that
// omit it).
func (c *Client) sendResult(resp *rpcResponse) (a2a.SendMessageResult, error) {
	ev, _, err := resultEvent(resp)
	if err != nil {
		return nil, err
	}
	switch r := ev.(type) {
	case *a2a.Task:
		return r, nil
	case *a2a.Message:
		return r, nil
	default:
		return nil, fmt.Errorf("compat03: message/send returned %T, want task or message: %w", ev, a2a.ErrInvalidAgentResponse)
	}
}

// resultEvent decodes a result payload that must carry exactly one event.
func resultEvent(resp *rpcResponse) (a2a.Event, bool, error) {
	trimmed := bytes.TrimSpace(resp.Result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, false, fmt.Errorf("compat03: agent returned an empty result: %w", a2a.ErrInvalidAgentResponse)
	}
	return eventFromResult(resp.Result)
}

// stream performs a streaming JSON-RPC call (message/stream or
// tasks/resubscribe) and translates SSE frames into SDK events.
func (c *Client) stream(ctx context.Context, method string, params func() (any, error)) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		p, err := params()
		if err != nil {
			yield(nil, err)
			return
		}
		req, err := c.newRequest(ctx, method, p, streamingMIME+", "+jsonMIME)
		if err != nil {
			yield(nil, err)
			return
		}
		resp, err := c.http.Do(req)
		if err != nil {
			yield(nil, transportError(ctx, method, err))
			return
		}

		// A conforming stream answers 200 + text/event-stream. Agents
		// that reject the method answer with a JSON-RPC error body (any
		// status); surface it as a clean error instead of feeding the
		// SSE parser JSON.
		if resp.StatusCode != http.StatusOK || !isEventStream(resp.Header.Get("Content-Type")) {
			yield(nil, c.jsonBodyError(method, resp))
			return
		}

		events := 0
		for frame, err := range Frames(ctx, resp) {
			if err != nil {
				yield(nil, err)
				return
			}
			if ctx.Err() != nil {
				yield(nil, ctx.Err())
				return
			}
			events++
			if events > maxEventsPerStream {
				yield(nil, fmt.Errorf("compat03: stream exceeded %d events", maxEventsPerStream))
				return
			}
			var env rpcResponse
			if err := json.Unmarshal(frame, &env); err != nil {
				yield(nil, fmt.Errorf("compat03: malformed stream frame: %w", err))
				return
			}
			if env.Error != nil {
				yield(nil, a2aError(env.Error))
				return
			}
			trimmed := bytes.TrimSpace(env.Result)
			if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
				continue // null-result keep-alive frames
			}
			ev, final, err := eventFromResult(env.Result)
			if err != nil {
				yield(nil, err)
				return
			}
			if ev == nil {
				continue
			}
			if !yield(ev, nil) {
				return
			}
			if final {
				return // final:true terminates the 0.3 stream
			}
		}
	}
}

// jsonBodyError drains a non-SSE response into the best available
// error: a decoded JSON-RPC error when present, otherwise an
// HTTP-status error quoting the body.
func (c *Client) jsonBodyError(method string, resp *http.Response) error {
	defer resp.Body.Close()
	body, err := readCapped(resp.Body)
	if err != nil {
		return fmt.Errorf("compat03: %s failed: %s (unreadable body: %v)", method, resp.Status, err)
	}
	if rpcErr := parseErrorBody(body); rpcErr != nil {
		return rpcErr
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("compat03: %s failed: %s: %s", method, resp.Status, snippet(body))
	}
	return fmt.Errorf("compat03: %s returned %q instead of an event stream: %s",
		method, resp.Header.Get("Content-Type"), snippet(body))
}

// isEventStream reports whether a Content-Type header names SSE.
func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(contentType)), streamingMIME)
}

// decodeResult unmarshals a success result into out, treating the
// expected-null result as an empty value.
func decodeResult(resp *rpcResponse, out any) error {
	trimmed := bytes.TrimSpace(resp.Result)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("compat03: decode result: %w", err)
	}
	return nil
}

// readCapped reads up to maxResponseBytes plus one byte (so oversize is
// detectable).
func readCapped(r io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
}

// snippet quotes the start of a body for error messages.
func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// parseErrorBody decodes a JSON-RPC error envelope from an arbitrary
// response body, returning nil when the body is not one.
func parseErrorBody(body []byte) error {
	var env rpcResponse
	if err := json.Unmarshal(body, &env); err != nil || env.Error == nil {
		return nil
	}
	return a2aError(env.Error)
}

// transportError classifies request failures, unwrapping context
// cancellation for the session layer.
func transportError(ctx context.Context, method string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("compat03: %s request failed: %w", method, err)
}

// codeSentinels maps 0.3 JSON-RPC error codes to the SDK sentinels the
// rest of the app already knows how to render.
var codeSentinels = map[int]error{
	-32700: a2a.ErrParseError,
	-32600: a2a.ErrInvalidRequest,
	-32601: a2a.ErrMethodNotFound,
	-32602: a2a.ErrInvalidParams,
	-32603: a2a.ErrInternalError,
	-32000: a2a.ErrServerError,
	-32001: a2a.ErrTaskNotFound,
	-32002: a2a.ErrTaskNotCancelable,
	-32003: a2a.ErrPushNotificationNotSupported,
	-32004: a2a.ErrUnsupportedOperation,
	-32005: a2a.ErrUnsupportedContentType,
}

// a2aError converts a wire error into an *a2a.Error wrapping the SDK
// sentinel (so errors.Is / FriendlyError keep working) while preserving
// the server's message and any data payload for display. Codes outside
// the spec still carry the code and message.
func a2aError(e *rpcError) error {
	if e == nil {
		return errors.New("compat03: unknown agent error")
	}
	sentinel, ok := codeSentinels[e.Code]
	if !ok {
		return fmt.Errorf("compat03: agent error %d: %s", e.Code, e.Message)
	}
	err := a2a.NewError(sentinel, e.Message)
	if len(e.Data) > 0 {
		err.WithDetails(map[string]any{"data": e.Data})
	}
	return err
}
