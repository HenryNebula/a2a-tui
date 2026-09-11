// Server wiring for the fixture agent: the a2a-go SDK request handler
// (JSON-RPC transport, in-memory task and push-config stores) plus the
// public agent card at /.well-known/agent-card.json.
package fixtureagent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/push"
)

// AgentName is the name advertised in the fixture's agent card.
const AgentName = "a2a-tui fixture agent"

// AgentCard returns the fixture's public card for the given base URL. The
// single JSONRPC interface points at baseURL itself, where NewJSONRPCHandler
// serves every A2A method.
func AgentCard(baseURL string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        AgentName,
		Description: "Deterministic A2A 1.0 agent scripted by the first keyword of each message; used to test a2a-tui.",
		Version:     "1.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(baseURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"text"},
		DefaultOutputModes: []string{"text", "application/a2ui+json"},
		Capabilities: a2a.AgentCapabilities{
			Streaming:         true,
			PushNotifications: true,
		},
		Skills: []a2a.AgentSkill{
			{
				ID:          "scripted-tasks",
				Name:        "Scripted task behaviors",
				Description: "First keyword selects: stream N (chunked working statuses), artifact, artifact append, inputreq (input-required round trip), push (slow status updates for webhook tests), slow SECONDS, fail, cancelme SECONDS, skills.",
				Tags:        []string{"stream", "artifact", "inputreq", "push", "slow", "fail", "cancelme", "skills"},
				Examples:    []string{"stream 5", "cancelme 30", "inputreq"},
			},
			{
				ID:          "a2ui-surfaces",
				Name:        "A2UI surfaces",
				Description: "a2ui form (all interactive components), a2ui dynamic (formatString data bindings with live updates) and a2ui list (list template over a data array), delivered as application/a2ui+json data parts.",
				Tags:        []string{"a2ui", "form", "dynamic", "list"},
				Examples:    []string{"a2ui form", "a2ui dynamic", "a2ui list"},
			},
			{
				ID:          "echo",
				Name:        "Echo",
				Description: "Any other message completes the task with \"echo: <text>\".",
				Tags:        []string{"echo"},
				Examples:    []string{"hello fixture"},
			},
		},
	}
}

// Server hosts one fixture agent instance. The card's interface URL is
// resolved from the base URL the server actually ends up on, so the same
// Server can be started on a fixed port or behind httptest.
type Server struct {
	agent   *Agent
	baseURL atomic.Value // string
}

// NewServer returns a fixture server with a fresh executor.
func NewServer() *Server {
	s := &Server{agent: NewAgent()}
	s.baseURL.Store("")
	return s
}

// Handler builds the fixture's HTTP surface: the JSON-RPC endpoint at "/"
// and the agent card at /.well-known/agent-card.json.
func (s *Server) Handler() http.Handler {
	capabilities := a2a.AgentCapabilities{
		Streaming:         true,
		PushNotifications: true,
	}
	requestHandler := a2asrv.NewHandler(s.agent,
		a2asrv.WithCapabilityChecks(&capabilities),
		a2asrv.WithPushNotifications(
			push.NewInMemoryStore(),
			// AllowPrivateNetworks is the whole point of the fixture: the
			// default SSRF guard refuses loopback/private webhook targets,
			// which would silently break localhost push tests. The fixture
			// only exists to be pointed at test webhooks.
			push.NewHTTPPushSender(&push.HTTPSenderConfig{
				AllowPrivateNetworks: true,
			}),
		),
	)

	cardProducer := a2asrv.AgentCardProducerFn(func(ctx context.Context) (*a2a.AgentCard, error) {
		return AgentCard(s.base()), nil
	})

	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewAgentCardHandler(cardProducer))
	mux.Handle("/", a2asrv.NewJSONRPCHandler(requestHandler))
	return mux
}

func (s *Server) base() string {
	v, _ := s.baseURL.Load().(string)
	return v
}

// Start serves the fixture on host:port (port 0 binds an ephemeral port)
// and returns its base URL plus a shutdown function. The shutdown waits for
// in-flight requests to drain (bounded).
func (s *Server) Start(host string, port int) (string, func(), error) {
	if host == "" {
		host = "127.0.0.1"
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", nil, fmt.Errorf("fixtureagent: listen: %w", err)
	}
	_, portStr, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		listener.Close()
		return "", nil, fmt.Errorf("fixtureagent: resolve port: %w", err)
	}
	base := "http://" + net.JoinHostPort(host, portStr)
	s.baseURL.Store(base)

	srv := &http.Server{Handler: s.Handler()}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("fixtureagent: serve: %v", err)
		}
	}()

	shutdown := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		<-done
	}
	return base, shutdown, nil
}

// Start serves a fresh fixture on host:port; see Server.Start.
func Start(host string, port int) (string, func(), error) {
	return NewServer().Start(host, port)
}

// StartTest serves the fixture behind httptest on an ephemeral loopback
// port; ts.URL is the agent's base URL and its card URL. Call ts.Close to
// stop it.
func (s *Server) StartTest() (*httptest.Server, error) {
	ts := httptest.NewServer(s.Handler())
	s.baseURL.Store(ts.URL)
	return ts, nil
}

// StartTest serves a fresh fixture behind httptest; see Server.StartTest.
func StartTest() (*httptest.Server, error) {
	return NewServer().StartTest()
}
