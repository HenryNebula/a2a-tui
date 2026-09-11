package agent

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/agent/connv1"
	"github.com/HenryNebula/a2a-tui/internal/compat03"
	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// AgentConn is the version-agnostic seam between the app and an agent.
// Both wire-protocol clients (connv1 now, compat03 in M4) implement it,
// translating whatever the agent speaks into the official SDK a2a types
// so the rest of the app never sees a protocol flavor.
type AgentConn interface {
	// Card returns the agent card, or nil when unavailable (0.3).
	Card() *a2a.AgentCard
	// CardSummary returns the sanitized display projection of the card.
	CardSummary() CardSummary
	// WireVersion reports the negotiated wire protocol ("1.0" or "0.3").
	WireVersion() string
	// BaseURL is the POST target the connection uses.
	BaseURL() string

	SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error)
	SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error]
	SubscribeToTask(ctx context.Context, id string) iter.Seq2[a2a.Event, error]
	GetTask(ctx context.Context, id string, historyLength *int) (*a2a.Task, error)
	ListTasks(ctx context.Context) (*a2a.ListTasksResponse, error)
	CancelTask(ctx context.Context, id string) (*a2a.Task, error)

	CreateTaskPushConfig(ctx context.Context, cfg *a2a.PushConfig) (*a2a.PushConfig, error)
	ListTaskPushConfigs(ctx context.Context, taskID string) ([]*a2a.PushConfig, error)
	DeleteTaskPushConfig(ctx context.Context, taskID, configID string) error

	GetExtendedAgentCard(ctx context.Context) (*a2a.AgentCard, error)

	// Destroy releases connection resources. It is idempotent.
	Destroy() error
}

// ConnOption customizes NewConn.
type ConnOption func(*connConfig)

type connConfig struct {
	wireLog *wirelog.Logger
}

// WithWireLog captures raw HTTP frames exchanged with the agent into log
// (see internal/wirelog). The logger is only read by the UI; NewConn wires
// it into the transport stack.
func WithWireLog(log *wirelog.Logger) ConnOption {
	return func(c *connConfig) { c.wireLog = log }
}

// NewConn builds an AgentConn for a resolved agent, dispatching on the
// negotiated wire protocol. httpClient may be nil (sensible defaults are
// used); its Timeout is cleared so long-lived SSE streams survive.
func NewConn(ctx context.Context, res *Resolved, httpClient *http.Client, opts ...ConnOption) (AgentConn, error) {
	var cfg connConfig
	for _, o := range opts {
		o(&cfg)
	}
	switch res.Wire {
	case WireV1:
		if res.CardV1 == nil {
			return nil, errors.New("v1.0 connection without a card")
		}
		iface := selectInterface(res.CardV1)
		if iface == nil {
			return nil, fmt.Errorf("%s has no A2A 1.x interface", res.BaseURL)
		}
		conn, err := connv1.New(ctx, res.CardV1, iface, httpClient, cfg.wireLog)
		if err != nil {
			return nil, err
		}
		return v1Adapter{
			Conn:    conn,
			summary: res.Summary,
			wire:    res.Wire,
			base:    res.BaseURL,
		}, nil
	case WireV03:
		if res.CardV03 == nil {
			return nil, errors.New("v0.3 connection without a card")
		}
		conn, err := compat03.New(ctx, res.BaseURL, res.CardV03, httpClient, cfg.wireLog)
		if err != nil {
			return nil, err
		}
		return v03Adapter{
			Client:  conn,
			summary: res.Summary,
			wire:    res.Wire,
			base:    res.BaseURL,
		}, nil
	default:
		return nil, fmt.Errorf("agent speaks unknown A2A wire version %q", res.Wire)
	}
}

// v1Adapter layers the display-oriented AgentConn methods over a raw
// connv1.Conn. The summary/wire/base values are frozen from resolution so
// the header and card pane always agree with what /connect reported.
type v1Adapter struct {
	*connv1.Conn
	summary CardSummary
	wire    string
	base    string
}

func (a v1Adapter) CardSummary() CardSummary { return a.summary }
func (a v1Adapter) WireVersion() string      { return a.wire }
func (a v1Adapter) BaseURL() string          { return a.base }

// v03Adapter layers the display-oriented AgentConn methods over a raw
// compat03.Client, exactly like v1Adapter does for connv1.Conn (the
// compat package must not import this one, so the adapter lives here).
type v03Adapter struct {
	*compat03.Client
	summary CardSummary
	wire    string
	base    string
}

func (a v03Adapter) CardSummary() CardSummary { return a.summary }
func (a v03Adapter) WireVersion() string      { return a.wire }
func (a v03Adapter) BaseURL() string          { return a.base }
