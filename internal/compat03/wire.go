package compat03

import (
	"encoding/json"
	"fmt"
)

// A2A 0.3 JSON-RPC method names (camelCase with slashes, unlike the
// PascalCase 1.x methods).
const (
	methodSend          = "message/send"
	methodStream        = "message/stream"
	methodGetTask       = "tasks/get"
	methodCancelTask    = "tasks/cancel"
	methodResubscribe   = "tasks/resubscribe"
	methodPushSet       = "tasks/pushNotificationConfig/set"
	methodPushGet       = "tasks/pushNotificationConfig/get"
	methodPushList      = "tasks/pushNotificationConfig/list"
	methodPushDelete    = "tasks/pushNotificationConfig/delete"
	streamingMIME       = "text/event-stream"
	jsonMIME            = "application/json"
	maxResponseBytes    = 1 << 20 // 1MB cap for blocking JSON-RPC bodies
	maxPartsPerMessage  = 1024    // defensive: hostile part arrays
	maxHistoryMessages  = 4096    // defensive: hostile history arrays
	maxArtifactsPerTask = 1024    // defensive: hostile artifact arrays
)

// rpcRequest is a JSON-RPC 2.0 request envelope. Ids are client-generated
// UUID strings.
type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// rpcResponse is a JSON-RPC 2.0 response envelope. Result stays raw: its
// shape depends on the method and is discriminated by the payload's
// "kind" member (see decodeResult).
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// rpcError is the JSON-RPC error object. Data is kept raw for display.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error renders the wire error for logs and tests.
func (e *rpcError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// wireMessage is the 0.3 Message object. Role is "user" or "agent".
// The readonly kind member ("message") is present on received objects
// and omitted when we send (servers accept both).
type wireMessage struct {
	MessageID      string         `json:"messageId"`
	TaskID         string         `json:"taskId,omitempty"`
	ContextID      string         `json:"contextId,omitempty"`
	Role           string         `json:"role"`
	Parts          []wirePart     `json:"parts"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Extensions     []string       `json:"extensions,omitempty"`
	ReferenceTasks []string       `json:"referenceTaskIds,omitempty"`
	Kind           string         `json:"kind,omitempty"`
}

// wirePart is one kind-discriminated content part:
//
//	{"kind":"text","text":…} | {"kind":"file","file":{…}} |
//	{"kind":"data","data":{…}}
//
// Data keeps raw JSON so non-object payloads (hostile or merely unusual)
// survive parsing. Raw retains the original bytes of received parts so
// unknown kinds can be surfaced losslessly.
type wirePart struct {
	Kind     string          `json:"kind"`
	Text     string          `json:"text,omitempty"`
	File     *wireFile       `json:"file,omitempty"`
	Data     json.RawMessage `json:"data,omitempty"`
	Metadata map[string]any  `json:"metadata,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

// UnmarshalJSON decodes a part and stashes the original bytes in Raw.
func (p *wirePart) UnmarshalJSON(b []byte) error {
	type wirePartAlias wirePart // avoid recursing into UnmarshalJSON
	var alias wirePartAlias
	if err := json.Unmarshal(b, &alias); err != nil {
		return err
	}
	*p = wirePart(alias)
	p.Raw = append(json.RawMessage(nil), b...)
	return nil
}

// wireFile is the nested file object of a file part. Per the 0.3 schema
// the base64 flavor carries "bytes" and the remote flavor "uri"; the
// spec's own streaming example instead uses "data" for base64, so both
// spellings are accepted defensively.
type wireFile struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// wireTask is the 0.3 Task object.
type wireTask struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId"`
	Status    wireStatus     `json:"status"`
	History   []wireMessage  `json:"history,omitempty"`
	Artifacts []wireArtifact `json:"artifacts,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Kind      string         `json:"kind,omitempty"`
}

// wireStatus is the status block of a task or status-update event.
// State values are camelCase-with-dashes: submitted, working,
// input-required, completed, failed, canceled, rejected, auth-required,
// unknown. Timestamp is an ISO 8601 string.
type wireStatus struct {
	State     string       `json:"state"`
	Message   *wireMessage `json:"message,omitempty"`
	Timestamp string       `json:"timestamp,omitempty"`
}

// wireArtifact is the 0.3 Artifact object.
type wireArtifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []wirePart     `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Extensions  []string       `json:"extensions,omitempty"`
}

// wireStatusUpdate is the "kind":"status-update" stream event.
type wireStatusUpdate struct {
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Kind      string         `json:"kind"`
	Status    wireStatus     `json:"status"`
	Final     bool           `json:"final"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// wireArtifactUpdate is the "kind":"artifact-update" stream event.
type wireArtifactUpdate struct {
	TaskID    string         `json:"taskId"`
	ContextID string         `json:"contextId"`
	Kind      string         `json:"kind"`
	Artifact  wireArtifact   `json:"artifact"`
	Append    bool           `json:"append,omitempty"`
	LastChunk bool           `json:"lastChunk,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// wireSendParams is the params object of message/send and message/stream.
type wireSendParams struct {
	Message  wireMessage            `json:"message"`
	Config   *wireSendConfiguration `json:"configuration,omitempty"`
	Metadata map[string]any         `json:"metadata,omitempty"`
}

// wireSendConfiguration is the 0.3 message configuration block.
type wireSendConfiguration struct {
	AcceptedOutputModes []string        `json:"acceptedOutputModes,omitempty"`
	HistoryLength       *int            `json:"historyLength,omitempty"`
	PushConfig          *wirePushConfig `json:"pushNotificationConfig,omitempty"`
	Blocking            *bool           `json:"blocking,omitempty"`
}

// wireTaskParams is the params shape shared by tasks/get, tasks/cancel,
// tasks/resubscribe and the push-notification config methods.
type wireTaskParams struct {
	ID         string         `json:"id"`
	HistoryLen *int           `json:"historyLength,omitempty"`
	ConfigID   string         `json:"pushNotificationConfigId,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// wirePushAuth describes how the agent should authenticate to the push
// webhook. Schemes is a list in 0.3 (a single scheme in 1.x).
type wirePushAuth struct {
	Schemes     []string `json:"schemes"`
	Credentials string   `json:"credentials,omitempty"`
}

// wirePushConfig is the 0.3 push notification config (no task id — that
// lives in the enclosing TaskPushNotificationConfig).
type wirePushConfig struct {
	ID    string        `json:"id,omitempty"`
	URL   string        `json:"url"`
	Token string        `json:"token,omitempty"`
	Auth  *wirePushAuth `json:"authentication,omitempty"`
}

// wireTaskPushConfig is the params of tasks/pushNotificationConfig/set,
// the result of .../get, and one element of the .../list result.
type wireTaskPushConfig struct {
	TaskID string         `json:"taskId"`
	Config wirePushConfig `json:"pushNotificationConfig"`
}

// resultKind reports the "kind" discriminator of a raw result payload,
// defaulting to shape sniffing for servers that omit it (some early 0.3
// implementations sent bare Tasks from message/send with no kind).
func resultKind(raw json.RawMessage) string {
	var probe struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &probe)
	switch probe.Kind {
	case "task", "status-update", "artifact-update", "message":
		return probe.Kind
	case "":
		return sniffKind(raw)
	default:
		return "unknown"
	}
}

// sniffKind guesses the payload type from its fields when "kind" is
// absent. Ordered from most to least specific.
func sniffKind(raw json.RawMessage) string {
	var probe struct {
		ArtifactID string          `json:"artifactId"`
		Artifact   json.RawMessage `json:"artifact"`
		TaskID     string          `json:"taskId"`
		Status     json.RawMessage `json:"status"`
		ID         string          `json:"id"`
		ContextID  string          `json:"contextId"`
		MessageID  string          `json:"messageId"`
		Parts      json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "unknown"
	}
	switch {
	case probe.ArtifactID != "" || probe.Artifact != nil:
		return "artifact-update"
	case probe.TaskID != "" && probe.Status != nil:
		return "status-update"
	case probe.MessageID != "" && probe.Parts != nil:
		return "message"
	case probe.ID != "" && probe.Status != nil:
		return "task"
	default:
		return "unknown"
	}
}

// capStrings truncates a hostile string slice in place.
func capStrings(in []string, limit int) []string {
	if len(in) <= limit {
		return in
	}
	return in[:limit]
}
