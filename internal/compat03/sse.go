package compat03

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"os"
	"time"
)

// SSE framing limits. A2A 0.3 streams carry one complete JSON-RPC
// response per event; the parser tolerates frames far larger than the
// default bufio.Scanner line cap but still bounded.
const (
	// maxLineBytes caps a single SSE line (1MB).
	maxLineBytes = 1 << 20
	// maxEventBytes caps the accumulated payload of one event (1MB).
	maxEventBytes = 1 << 20
	// idleReadTimeout bounds how long a stream may stay silent before
	// the client gives up (deadline reset on every read).
	idleReadTimeout = 60 * time.Second
	// sseReadBuffer is the bufio buffer size; lines longer than this are
	// accumulated across ErrBufferFull reads.
	sseReadBuffer = 64 << 10
)

// Frame is the raw payload of one server-sent event: the concatenation
// (newline-joined) of every `data:` line in the event.
type Frame []byte

// readDeadliner is implemented by response bodies whose transport can
// bound reads in time (HTTP/2 bodies do; plain HTTP/1 bodies do not,
// those streams rely on context cancellation instead).
type readDeadliner interface {
	SetReadDeadline(time.Time) error
}

// Frames reads a text/event-stream body, yielding one Frame per event.
//
// It is hand-rolled because A2A frames routinely exceed bufio.Scanner's
// 64KB default and the SDK offers no reusable SSE reader:
//
//   - multiple `data:` lines per event are joined with "\n" per the SSE
//     spec; `event:`, `id:` and `retry:` lines, `:` comments, keep-alive
//     blank events, and CRLF endings are tolerated;
//   - reads carry a 60s idle deadline (reset per read, where the
//     transport supports it) so silent streams eventually surface an
//     error instead of hanging forever;
//   - ctx cancellation stops iteration promptly;
//   - lines and events over 1MB produce an error, never a panic.
//
// Frames owns resp.Body: it is closed when iteration ends.
func Frames(ctx context.Context, resp *http.Response) iter.Seq2[Frame, error] {
	return func(yield func(Frame, error) bool) {
		if resp == nil || resp.Body == nil {
			yield(nil, errors.New("compat03: SSE response has no body"))
			return
		}
		defer resp.Body.Close()

		deadliner, _ := resp.Body.(readDeadliner)
		reader := bufio.NewReaderSize(resp.Body, sseReadBuffer)

		var (
			dataLines [][]byte
			dataBytes int
		)
		flush := func() bool {
			if len(dataLines) == 0 {
				return true // keep-alive or comment-only event
			}
			frame := bytes.Join(dataLines, []byte("\n"))
			dataLines, dataBytes = nil, 0
			return yield(Frame(frame), nil)
		}

		for {
			if err := ctx.Err(); err != nil {
				yield(nil, fmt.Errorf("compat03: stream canceled: %w", err))
				return
			}
			if deadliner != nil {
				_ = deadliner.SetReadDeadline(time.Now().Add(idleReadTimeout))
			}

			line, err := readLine(reader)
			if err != nil {
				if errors.Is(err, io.EOF) {
					// Tolerate streams that close without a trailing
					// blank line: dispatch any pending event.
					if len(dataLines) > 0 && !flush() {
						return
					}
					return
				}
				yield(nil, readError(err))
				return
			}

			switch {
			case len(line) == 0: // event boundary
				if !flush() {
					return
				}
			case line[0] == ':': // comment / keep-alive
				continue
			default:
				field, value, ok := bytes.Cut(line, []byte(":"))
				if !ok {
					continue // stray line without a colon: ignore
				}
				value = bytes.TrimPrefix(value, []byte(" "))
				if !bytes.Equal(field, []byte("data")) {
					continue // event:/id:/retry:/unknown fields: ignored
				}
				dataBytes += len(value)
				if dataBytes > maxEventBytes {
					yield(nil, fmt.Errorf("compat03: SSE event exceeds %d bytes", maxEventBytes))
					return
				}
				dataLines = append(dataLines, bytes.Clone(value))
			}
		}
	}
}

// readLine reads one line, stripping the trailing newline and any CR.
// Lines longer than the bufio buffer are accumulated (copied — chunks
// alias the reader's internal buffer); beyond maxLineBytes the read
// errors out. The returned slice aliases the reader buffer when the line
// fit in one read; callers must copy anything they retain.
func readLine(r *bufio.Reader) ([]byte, error) {
	var acc []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if err == nil || (errors.Is(err, io.EOF) && (len(acc) > 0 || len(chunk) > 0)) {
			// Complete line, or the final unterminated line at EOF.
			if len(acc)+len(chunk) > maxLineBytes {
				return nil, fmt.Errorf("compat03: SSE line exceeds %d bytes", maxLineBytes)
			}
			return combine(acc, chunk), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			if acc == nil {
				acc = make([]byte, 0, 2*len(chunk))
			}
			acc = append(acc, chunk...)
			if len(acc) > maxLineBytes {
				return nil, fmt.Errorf("compat03: SSE line exceeds %d bytes", maxLineBytes)
			}
			continue
		}
		return nil, err
	}
}

// combine joins accumulated overflow bytes with the final chunk and
// strips the EOL. When acc is empty the chunk is returned as-is (still
// aliasing the reader buffer — see readLine).
func combine(acc, chunk []byte) []byte {
	if len(acc) > 0 {
		chunk = append(acc, chunk...)
	}
	return trimEOL(chunk)
}

// trimEOL removes one trailing \n and then one trailing \r.
func trimEOL(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}

// readError classifies a body-read failure: idle timeouts are reported
// as such, deadlines surface verbatim, everything else wraps the cause.
func readError(err error) error {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("compat03: stream idle for over %s", idleReadTimeout)
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("compat03: stream idle for over %s", idleReadTimeout)
	}
	return fmt.Errorf("compat03: reading SSE stream: %w", err)
}
