// Package rpcconsole posts raw JSON-RPC 2.0 requests to an A2A agent and
// returns the raw bytes back. It powers the console pane: the user picks a
// method (or types any string), supplies arbitrary params JSON, and sees
// exactly what the agent answered — including SSE stream frames — with no
// translation between them and the wire.
//
// A Console is safe for concurrent use. Every exchange (success, protocol
// error, transport failure) lands in a small history ring so the UI can
// cycle past requests.
package rpcconsole

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HenryNebula/a2a-tui/internal/compat03"
)

// Limits applied to every console call.
const (
	// HistorySize is how many past exchanges the ring retains.
	HistorySize = 50
	// MaxResponseBytes caps a blocking JSON response body.
	MaxResponseBytes = 1 << 20 // 1MB
	// MaxFrames caps the SSE frames collected from one stream.
	MaxFrames = 100
	// MaxStreamBytes caps the total SSE payload collected from one stream.
	MaxStreamBytes = 1 << 20 // 1MB
	// CallTimeout bounds one whole call, SSE streams included.
	CallTimeout = 30 * time.Second

	jsonMIME      = "application/json"
	streamingMIME = "text/event-stream"
	acceptHeader  = "application/json, text/event-stream"
)

// Errors reported by Call. Transport and decoding failures are wrapped
// contextually; these mark the collection caps.
var (
	// ErrTooManyFrames reports that the stream emitted more than MaxFrames
	// data frames; the first MaxFrames are still returned.
	ErrTooManyFrames = errors.New("rpcconsole: stream exceeded the frame cap")
	// ErrStreamTooLarge reports that the stream payload exceeded
	// MaxStreamBytes; the frames collected so far are still returned.
	ErrStreamTooLarge = errors.New("rpcconsole: stream exceeded the size cap")
	// ErrResponseTooLarge reports a blocking body over MaxResponseBytes;
	// the first MaxResponseBytes are still returned.
	ErrResponseTooLarge = errors.New("rpcconsole: response exceeds the size cap")
)

// Exchange is one console call as recorded in the history ring. Either Err
// is non-empty (transport failure or cap exceeded) or Resp/Frames carry
// what the agent answered. HTTP and JSON-RPC level errors are NOT Go
// errors here: a raw console shows them as data.
type Exchange struct {
	// Method is the JSON-RPC method that was called.
	Method string
	// Params is the raw params JSON exactly as sent (nil when omitted).
	Params []byte
	// Resp is the raw body of a blocking response.
	Resp []byte
	// Frames holds the data frames of an SSE response.
	Frames []string
	// Status is the HTTP status code (0 on transport failure).
	Status int
	// SSE reports whether the answer was an event stream.
	SSE bool
	// Err carries transport failures and cap errors.
	Err string
	// At is when the call started.
	At time.Time
}

// Console posts raw JSON-RPC requests to one POST target. The http.Client
// should be the session's client (wirelog-wrapped) so every console call
// also lands in the wire log; a nil client falls back to the default.
type Console struct {
	base   string
	client *http.Client

	nextID atomic.Int64

	mu      sync.Mutex
	history []Exchange
}

// New returns a Console posting to baseURL (the session's POST target).
func New(baseURL string, client *http.Client) *Console {
	if client == nil {
		client = http.DefaultClient
	}
	return &Console{base: baseURL, client: client}
}

// BaseURL is the POST target.
func (c *Console) BaseURL() string { return c.base }

// rpcRequest is the JSON-RPC 2.0 request envelope. Ids are client-side
// incrementing integers so requests are correlatable in the wire log.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Call posts one JSON-RPC request and returns the raw answer. versionHeader
// is sent as the A2A-Version header when non-empty (the v1.0 dialect sends
// "1.0"; 0.3 sends nothing). auth carries extra headers, e.g. Authorization
// from stored credentials.
//
// A text/event-stream answer is drained server-sent-event style: each data
// frame is returned in frames (capped at MaxFrames / MaxStreamBytes; the
// partial result is returned alongside ErrTooManyFrames/ErrStreamTooLarge).
// Any other content type is returned verbatim in respBody (capped at
// MaxResponseBytes the same way). The call is bounded by CallTimeout.
func (c *Console) Call(ctx context.Context, method string, params json.RawMessage, versionHeader string, auth map[string]string) (respBody []byte, frames []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()

	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: c.nextID.Add(1), Method: method, Params: normalizeParams(params)})
	if err != nil {
		return nil, nil, fmt.Errorf("rpcconsole: encode %s request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("rpcconsole: build %s request: %w", method, err)
	}
	req.Header.Set("Content-Type", jsonMIME)
	req.Header.Set("Accept", acceptHeader)
	if versionHeader != "" {
		req.Header.Set("A2A-Version", versionHeader)
	}
	for k, v := range auth {
		if k == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	ex := Exchange{Method: method, Params: paramsCopy(params), At: time.Now()}
	defer func() {
		ex.Resp, ex.Frames, ex.Err = respBody, frames, errorString(err)
		c.record(ex)
	}()

	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, fmt.Errorf("rpcconsole: %s aborted: %w", method, ctx.Err())
		}
		return nil, nil, fmt.Errorf("rpcconsole: %s request failed: %w", method, err)
	}
	ex.Status = resp.StatusCode

	if isEventStream(resp.Header.Get("Content-Type")) {
		ex.SSE = true
		frames, err = collectFrames(ctx, resp)
		return nil, frames, err
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err = readCapped(resp.Body)
	return respBody, nil, err
}

// History returns a copy of the retained exchanges, oldest first.
func (c *Console) History() []Exchange {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Exchange, len(c.history))
	copy(out, c.history)
	return out
}

// record appends one exchange to the ring.
func (c *Console) record(ex Exchange) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.history) < HistorySize {
		c.history = append(c.history, ex)
		return
	}
	copy(c.history, c.history[1:])
	c.history[len(c.history)-1] = ex
}

// normalizeParams drops params that are absent or only whitespace so the
// request omits the member entirely (some agents reject explicit null).
func normalizeParams(params json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(params)) == 0 || bytes.Equal(bytes.TrimSpace(params), []byte("null")) {
		return nil
	}
	return params
}

// paramsCopy retains the params exactly as sent for the history ring.
func paramsCopy(params json.RawMessage) []byte {
	if len(params) == 0 {
		return nil
	}
	return append([]byte(nil), params...)
}

// collectFrames drains an SSE body into frames via the compat03 reader
// (multi-line data joining, comments, CRLF and idle timeouts handled
// there), applying the console's frame and size caps. Frames owns the
// response body; breaking out of the iteration closes it.
func collectFrames(ctx context.Context, resp *http.Response) ([]string, error) {
	var (
		frames []string
		total  int
		capErr error
	)
	for frame, err := range compat03.Frames(ctx, resp) {
		if err != nil {
			if capErr != nil {
				return frames, capErr // cap hit earlier: report it over the follow-on read error
			}
			return frames, fmt.Errorf("rpcconsole: reading stream: %w", err)
		}
		frames = append(frames, string(frame))
		total += len(frame)
		if len(frames) >= MaxFrames {
			capErr = ErrTooManyFrames
			break
		}
		if total >= MaxStreamBytes {
			capErr = ErrStreamTooLarge
			break
		}
	}
	return frames, capErr
}

// readCapped reads up to MaxResponseBytes plus one byte so oversize is
// detectable, returning the capped body and ErrResponseTooLarge.
func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxResponseBytes+1))
	if err != nil {
		return data, fmt.Errorf("rpcconsole: reading response: %w", err)
	}
	if len(data) > MaxResponseBytes {
		return data[:MaxResponseBytes], ErrResponseTooLarge
	}
	return data, nil
}

// isEventStream reports whether a Content-Type header names SSE.
func isEventStream(contentType string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.ToLower(contentType)), streamingMIME)
}

// errorString renders err for the history ring ("" when nil).
func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
