// Package pushsrv runs a2a-tui's local push-notification webhook: a small
// loopback HTTP listener that authenticates the agent with a shared token,
// decodes each posted body (A2A 1.0 StreamResponse or legacy 0.3 event
// flavor) and delivers it as an SDK a2a.Event to a callback.
//
// Agents push by POSTing one stream-event object per request. The listener
// binds 127.0.0.1, so remote agents cannot reach it directly — tunnel the
// port (SSH/cloudflared/ngrok) and advertise the public URL via
// --push-public-url.
package pushsrv

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Server parameters.
const (
	// Path is the webhook path agents POST to.
	Path = "/push"
	// MaxBodyBytes caps one push body (spec-sized events are far smaller).
	MaxBodyBytes = 1 << 20 // 1MB
	// DefaultAddr is the listen address (ephemeral loopback port).
	DefaultAddr = "127.0.0.1:0"
	// shutdownTimeout bounds the graceful HTTP shutdown.
	shutdownTimeout = 3 * time.Second
	// TokenHexBytes is the token entropy (16 bytes → 32 hex chars).
	TokenHexBytes = 16
)

// notificationTokenHeader carries the push token in the SDK's spelling:
// the official a2a-go push sender sets it instead of Authorization.
var notificationTokenHeader = http.CanonicalHeaderKey("A2A-Notification-Token")

// Server is one running webhook listener. Create one with Start.
type Server struct {
	srv   *http.Server
	ln    net.Listener
	url   string // advertised URL (may point at a tunnel)
	token string

	shutdownOnce sync.Once
}

// NewToken mints a fresh 32-hex-character token from crypto/rand.
func NewToken() (string, error) {
	buf := make([]byte, TokenHexBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("pushsrv: token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Start boots the webhook listener. addr may be "" (DefaultAddr, an
// ephemeral loopback port); token must be non-empty (see NewToken).
// publicURL overrides the URL handed to agents (tunnels); "" derives
// http://<listener>/push. deliver receives each decoded event on the
// serving goroutine — keep it cheap (a non-blocking channel send is ideal).
func Start(addr, token, publicURL string, deliver func(a2a.Event)) (*Server, error) {
	if deliver == nil {
		return nil, errors.New("pushsrv: nil deliver callback")
	}
	if token == "" {
		return nil, errors.New("pushsrv: empty push token")
	}
	if addr == "" {
		addr = DefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pushsrv: listen: %w", err)
	}
	advertised := publicURL
	if advertised == "" {
		advertised = "http://" + ln.Addr().String() + Path
	}
	s := &Server{ln: ln, url: advertised, token: token}
	mux := http.NewServeMux()
	mux.HandleFunc(Path, s.handle(deliver))
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		// Serve returns http.ErrServerClosed on Shutdown; anything else
		// means the listener died (port vanished, socket closed) and the
		// server is simply over.
		_ = s.srv.Serve(ln)
	}()
	return s, nil
}

// URL returns the advertised webhook URL agents should POST to.
func (s *Server) URL() string { return s.url }

// Addr returns the concrete listener address (host:port).
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Shutdown stops the listener and waits (bounded) for in-flight requests.
// It is idempotent and safe on a nil server.
func (s *Server) Shutdown() {
	if s == nil {
		return
	}
	s.shutdownOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = s.srv.Shutdown(ctx)
	})
}

// handle builds the /push POST handler: token check → size-capped read →
// decode → deliver.
func (s *Server) handle(deliver func(a2a.Event)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "pushsrv: POST only", http.StatusMethodNotAllowed)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "pushsrv: bad or missing token", http.StatusUnauthorized)
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		ev, err := DecodeEvent(body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		deliver(ev)
		w.WriteHeader(http.StatusNoContent)
	}
}

// authorized constant-time-compares the shared token against the
// credential spellings agents actually send: the A2A-Notification-Token
// header (official SDK push sender) and Authorization: Bearer.
func (s *Server) authorized(r *http.Request) bool {
	candidates := []string{r.Header.Get(notificationTokenHeader)}
	if cred, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		candidates = append(candidates, strings.TrimSpace(cred))
	}
	for _, c := range candidates {
		if c != "" && subtle.ConstantTimeCompare([]byte(c), []byte(s.token)) == 1 {
			return true
		}
	}
	return false
}

// readBody reads the request body capped at MaxBodyBytes, answering the
// request itself on overflow or read error.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "pushsrv: body exceeds 1MB cap", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "pushsrv: read body: "+err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return body, true
}
