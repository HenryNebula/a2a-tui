// Package wirelog captures raw HTTP request/response pairs exchanged with
// an agent into an in-memory ring buffer. It exists so the TUI can show
// exactly what went over the wire (raw JSON-RPC frames, SSE chunks) and so
// error rendering can quote the offending response body.
//
// A Logger wraps an http.RoundTripper via Logger.Transport; capture is
// best-effort: bodies are capped (64KB per direction), sensitive headers
// are redacted, and streaming response bodies are teed while the real
// consumer reads them, so the entry completes when the stream closes.
package wirelog

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits applied to each captured frame.
const (
	// MaxBodyBytes is the per-direction body capture cap.
	MaxBodyBytes = 64 << 10 // 64KB
	// DefaultRingSize is the default number of retained entries.
	DefaultRingSize = 200
	// readHardCap bounds how much of an outgoing request body is read for
	// capture even though only MaxBodyBytes are stored: outgoing bodies are
	// locally constructed and small, but hostile helpers could lie.
	readHardCap = 1 << 20 // 1MB
)

// redactHeaders lists headers whose values must never be captured: the
// standard credential headers, the A2A push-notification webhook token,
// and the spellings common API-key gateways use.
var redactHeaders = map[string]bool{
	"authorization":          true,
	"proxy-authorization":    true,
	"cookie":                 true,
	"set-cookie":             true,
	"a2a-notification-token": true,
	"x-api-key":              true,
	"api-key":                true,
	"x-auth-token":           true,
}

// Entry is one captured request/response exchange. Either Err is non-empty
// (transport failure) or Status is the HTTP status code. ReqTrunc and
// RespTrunc report that a body exceeded MaxBodyBytes and was cut.
type Entry struct {
	At          time.Time
	Method      string
	URL         string
	ReqHeaders  http.Header
	ReqBody     []byte
	Duration    time.Duration
	Status      int
	RespHeaders http.Header
	RespBody    []byte
	Err         string
	ReqTrunc    bool
	RespTrunc   bool
}

// Logger retains the last ringSize entries. It is safe for concurrent use.
// The zero value is not useful; use New.
type Logger struct {
	enabled atomic.Bool
	mu      sync.Mutex
	entries []Entry
	ring    int
}

// New returns a Logger retaining the last ringSize entries (minimum 1,
// default DefaultRingSize when ringSize <= 0). Capture starts enabled.
func New(ringSize int) *Logger {
	if ringSize <= 0 {
		ringSize = DefaultRingSize
	}
	l := &Logger{ring: ringSize}
	l.enabled.Store(true)
	return l
}

// Enabled reports whether capture is on.
func (l *Logger) Enabled() bool { return l.enabled.Load() }

// SetEnabled turns capture on or off. Entries already captured are kept.
func (l *Logger) SetEnabled(on bool) { l.enabled.Store(on) }

// Snapshot returns a copy of the retained entries, oldest first.
func (l *Logger) Snapshot() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Entry, len(l.entries))
	copy(out, l.entries)
	return out
}

// Last returns the most recent entry, or nil when nothing is captured.
func (l *Logger) Last() *Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) == 0 {
		return nil
	}
	e := l.entries[len(l.entries)-1]
	return &e
}

// add appends an entry to the ring, evicting the oldest when full.
func (l *Logger) add(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.entries) < l.ring {
		l.entries = append(l.entries, e)
		return
	}
	copy(l.entries, l.entries[1:])
	l.entries[len(l.entries)-1] = e
}

// Transport returns an http.RoundTripper that captures exchanges through
// base (http.DefaultTransport when nil) into l. When capture is disabled
// the round tripper is pass-through.
func (l *Logger) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &loggingTransport{base: base, log: l}
}

type loggingTransport struct {
	base http.RoundTripper
	log  *Logger
}

// RoundTrip implements http.RoundTripper.
func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.log.enabled.Load() {
		return t.base.RoundTrip(req)
	}

	reqBody, reqTrunc, consumed := captureRequestBody(req)

	clone := req.Clone(req.Context())
	if consumed {
		// The original stream was drained for capture (no GetBody): the
		// clone must send a fresh reader over the captured bytes. When
		// the request is replayable the untouched original body rides
		// along — swapping it for the (capped) capture would truncate
		// oversized requests on the wire.
		clone.Body = io.NopCloser(bytes.NewReader(reqBody))
		clone.ContentLength = int64(len(reqBody))
	}

	start := time.Now()
	resp, err := t.base.RoundTrip(clone)
	dur := time.Since(start)

	entry := Entry{
		At:         start,
		Method:     req.Method,
		URL:        req.URL.String(),
		ReqHeaders: redact(req.Header),
		ReqBody:    capBytes(reqBody),
		ReqTrunc:   reqTrunc,
		Duration:   dur,
	}
	if err != nil {
		entry.Err = err.Error()
		t.log.add(entry)
		return resp, err
	}

	entry.Status = resp.StatusCode
	entry.RespHeaders = redact(resp.Header)
	capture := &bodyCapture{}
	resp.Body = &captureReadCloser{inner: resp.Body, capture: capture, entry: &entry, log: t.log}
	return resp, nil
}

// captureRequestBody captures a copy of the request body, reporting the
// bytes read, whether they exceeded MaxBodyBytes, and whether the original
// stream was consumed. It never consumes the original stream when the
// request is replayable (GetBody set — the common case for JSON-RPC
// posts): the caller then keeps sending the original body in full while
// the capture may be capped. A non-replayable body must be drained to be
// captured, and the caller is expected to send the captured bytes instead.
func captureRequestBody(req *http.Request) (data []byte, trunc, consumed bool) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, false, false
	}
	if req.GetBody != nil {
		if r, err := req.GetBody(); err == nil {
			data, _ = io.ReadAll(io.LimitReader(r, MaxBodyBytes+1))
			return data, len(data) > MaxBodyBytes, false
		}
	}
	// Non-replayable body: read it fully (bounded) and hand the caller a
	// fresh reader over the same bytes via the cloned request.
	data, _ = io.ReadAll(io.LimitReader(req.Body, readHardCap+1))
	if data == nil {
		data = []byte{}
	}
	return data, len(data) > MaxBodyBytes, true
}

// capBytes trims b to MaxBodyBytes.
func capBytes(b []byte) []byte {
	if len(b) > MaxBodyBytes {
		return b[:MaxBodyBytes]
	}
	return b
}

// redact copies headers, replacing the values of sensitive names.
func redact(h http.Header) http.Header {
	if h == nil {
		return nil
	}
	out := make(http.Header, len(h))
	for k, vals := range h {
		if redactHeaders[strings.ToLower(k)] {
			out[k] = []string{"REDACTED"}
			continue
		}
		out[k] = append([]string(nil), vals...)
	}
	return out
}

// bodyCapture accumulates response bytes up to MaxBodyBytes.
type bodyCapture struct {
	buf       bytes.Buffer
	truncated bool
}

func (c *bodyCapture) write(p []byte) {
	if c.buf.Len() >= MaxBodyBytes {
		c.truncated = true
		return
	}
	room := MaxBodyBytes - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return
	}
	c.buf.Write(p)
}

// captureReadCloser tees the response body into the pending Entry and
// commits it to the logger exactly once, when the body hits EOF or Close.
type captureReadCloser struct {
	inner   io.ReadCloser
	capture *bodyCapture
	entry   *Entry
	log     *Logger
	once    sync.Once
}

// Read implements io.Reader.
func (c *captureReadCloser) Read(p []byte) (int, error) {
	n, err := c.inner.Read(p)
	if n > 0 {
		c.capture.write(p[:n])
	}
	if err == io.EOF {
		c.commit()
	}
	return n, err
}

// Close implements io.Closer.
func (c *captureReadCloser) Close() error {
	err := c.inner.Close()
	c.commit()
	return err
}

func (c *captureReadCloser) commit() {
	c.once.Do(func() {
		c.entry.RespBody = c.capture.buf.Bytes()
		c.entry.RespTrunc = c.capture.truncated
		c.log.add(*c.entry)
	})
}
