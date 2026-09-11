// Package connv1 implements the v1.0 (A2A 1.0, JSON-RPC over HTTP) client
// on top of the official SDK (github.com/a2aproject/a2a-go/v2). All SDK
// contact for the v1 wire path is confined to this package.
//
// The SDK client is constructed from a pinned single-interface copy of the
// agent card so the connection targets exactly the URL the resolver chose,
// and a2aclient.WithDefaultsDisabled plus WithJSONRPCTransport keeps the
// factory from negotiating other transports. The A2A-Version service
// parameter is attached automatically by the SDK on every call (it becomes
// the A2A-Version HTTP header); anything extra (Authorization, extensions)
// rides through a2aclient.AttachServiceParams on the call context.
package connv1

import (
	"context"
	"errors"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// Conn is a v1.0 connection. It is a thin named-method facade over
// *a2aclient.Client; the agent package adapts it to agent.AgentConn.
type Conn struct {
	client *a2aclient.Client
	card   *a2a.AgentCard
	iface  *a2a.AgentInterface
	logger *wirelog.Logger
}

// New builds a Conn for the given card and interface. httpClient may be
// nil; when provided its Timeout is cleared (SSE streams outlive short
// client timeouts — cancellation is context-driven) and its Transport is
// wrapped by log's capturing round tripper when log is non-nil.
func New(ctx context.Context, card *a2a.AgentCard, iface *a2a.AgentInterface, httpClient *http.Client, log *wirelog.Logger) (*Conn, error) {
	if card == nil {
		return nil, errors.New("connv1: nil card")
	}
	if iface == nil || iface.ProtocolBinding != a2a.TransportProtocolJSONRPC {
		return nil, errors.New("connv1: only JSONRPC interfaces are supported")
	}

	hc := &http.Client{}
	if httpClient != nil {
		*hc = *httpClient
	}
	hc.Timeout = 0 // request lifetimes are governed by contexts
	transport := hc.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if log != nil {
		transport = log.Transport(transport)
	}
	hc.Transport = transport

	// Pin the connection to exactly the resolved interface. The SDK
	// factory otherwise sorts all card interfaces by protocol version and
	// may pick a different URL or binding than the resolver did.
	pinnedIface := *iface
	pinnedCard := *card
	pinnedCard.SupportedInterfaces = []*a2a.AgentInterface{&pinnedIface}

	client, err := a2aclient.NewFromCard(ctx, &pinnedCard,
		a2aclient.WithDefaultsDisabled(),
		a2aclient.WithJSONRPCTransport(hc),
	)
	if err != nil {
		return nil, err
	}
	return &Conn{client: client, card: &pinnedCard, iface: &pinnedIface, logger: log}, nil
}

// Card returns the (pinned) agent card the connection was built from.
func (c *Conn) Card() *a2a.AgentCard { return c.card }

// Interface returns the interface the connection posts to.
func (c *Conn) Interface() *a2a.AgentInterface { return c.iface }

// WireLog returns the wire logger, or nil.
func (c *Conn) WireLog() *wirelog.Logger { return c.logger }

// SendMessage implements the SendMessage JSON-RPC method.
func (c *Conn) SendMessage(ctx context.Context, req *a2a.SendMessageRequest) (a2a.SendMessageResult, error) {
	return c.client.SendMessage(ctx, req)
}

// SendStreamingMessage implements the SendStreamingMessage JSON-RPC method
// (SSE). Note: the SDK transparently falls back to blocking SendMessage
// when the card declares no streaming capability.
func (c *Conn) SendStreamingMessage(ctx context.Context, req *a2a.SendMessageRequest) iter.Seq2[a2a.Event, error] {
	return c.client.SendStreamingMessage(ctx, req)
}

// SubscribeToTask implements the SubscribeToTask JSON-RPC method (SSE).
// Per the spec the agent emits a full Task snapshot as the first event.
func (c *Conn) SubscribeToTask(ctx context.Context, id string) iter.Seq2[a2a.Event, error] {
	return c.client.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: a2a.TaskID(id)})
}

// GetTask implements the GetTask JSON-RPC method. historyLength may be
// nil (server default).
func (c *Conn) GetTask(ctx context.Context, id string, historyLength *int) (*a2a.Task, error) {
	return c.client.GetTask(ctx, &a2a.GetTaskRequest{ID: a2a.TaskID(id), HistoryLength: historyLength})
}

// ListTasks implements the ListTasks JSON-RPC method with server-default
// pagination.
func (c *Conn) ListTasks(ctx context.Context) (*a2a.ListTasksResponse, error) {
	return c.client.ListTasks(ctx, &a2a.ListTasksRequest{})
}

// CancelTask implements the CancelTask JSON-RPC method.
func (c *Conn) CancelTask(ctx context.Context, id string) (*a2a.Task, error) {
	return c.client.CancelTask(ctx, &a2a.CancelTaskRequest{ID: a2a.TaskID(id)})
}

// CreateTaskPushConfig implements CreateTaskPushNotificationConfig.
func (c *Conn) CreateTaskPushConfig(ctx context.Context, cfg *a2a.PushConfig) (*a2a.PushConfig, error) {
	return c.client.CreateTaskPushConfig(ctx, cfg)
}

// ListTaskPushConfigs implements ListTaskPushNotificationConfigs.
func (c *Conn) ListTaskPushConfigs(ctx context.Context, taskID string) ([]*a2a.PushConfig, error) {
	return c.client.ListTaskPushConfigs(ctx, &a2a.ListTaskPushConfigRequest{TaskID: a2a.TaskID(taskID)})
}

// DeleteTaskPushConfig implements DeleteTaskPushNotificationConfig.
func (c *Conn) DeleteTaskPushConfig(ctx context.Context, taskID, configID string) error {
	return c.client.DeleteTaskPushConfig(ctx, &a2a.DeleteTaskPushConfigRequest{TaskID: a2a.TaskID(taskID), ID: configID})
}

// GetExtendedAgentCard implements the GetExtendedAgentCard JSON-RPC method.
// The SDK short-circuits with ErrExtendedCardNotConfigured when the card
// does not advertise the capability.
func (c *Conn) GetExtendedAgentCard(ctx context.Context) (*a2a.AgentCard, error) {
	return c.client.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
}

// Destroy releases the connection. It is idempotent.
func (c *Conn) Destroy() error { return c.client.Destroy() }
