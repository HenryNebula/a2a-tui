package a2ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Protocol version strings accepted on the wire.
const (
	// VersionV1 is the A2UI v1.0 protocol version.
	VersionV1 = "v1.0"
	// VersionV091 is the A2UI v0.9.1 protocol version.
	VersionV091 = "v0.9.1"
	// VersionV09 is the A2UI v0.9 protocol version (wire-identical to
	// v0.9.1 for the messages this engine handles).
	VersionV09 = "v0.9"
)

// MaxEnvelopeBytes bounds a single decoded envelope (1 MiB) so hostile
// payloads cannot exhaust memory.
const MaxEnvelopeBytes = 1 << 20

// ErrUnknownVersion reports an envelope version the engine does not know;
// the envelope is still processed on a best-effort basis.
var ErrUnknownVersion = errors.New("a2ui: unknown protocol version")

// CreateSurface is the createSurface payload. v1.0 allows an initial
// components list and data model; v0.9.1 sends them separately.
type CreateSurface struct {
	SurfaceID     string      `json:"surfaceId"`
	CatalogID     string      `json:"catalogId,omitempty"`
	SendDataModel bool        `json:"sendDataModel,omitempty"`
	Components    []Component `json:"components,omitempty"`
	DataModel     any         `json:"dataModel,omitempty"`
}

// UpdateComponents upserts components by ID into a surface's adjacency list.
type UpdateComponents struct {
	SurfaceID  string      `json:"surfaceId"`
	Components []Component `json:"components"`
}

// UpdateDataModel replaces the value at Path (whole model when Path is "" or
// "/"); Value nil (or absent, v0.9.1) deletes the key at Path.
type UpdateDataModel struct {
	SurfaceID string `json:"surfaceId"`
	Path      string `json:"path,omitempty"`
	Value     any    `json:"value,omitempty"`
	// HasValue distinguishes an omitted value from an explicit null
	// (v0.9.1 treats omission as delete; v1.0 requires the key).
	HasValue bool `json:"-"`
}

// DeleteSurface removes a surface and all of its state.
type DeleteSurface struct {
	SurfaceID string `json:"surfaceId"`
}

// CallRendererFunction is the agent-initiated renderer function call
// ({"functionCallId", "callFunction": {call, catalogId, args}}).
type CallRendererFunction struct {
	FunctionCallID string `json:"functionCallId"`
	CallFunction   Call   `json:"callFunction"`
}

// FunctionResponsePayload is the value-or-error body shared by
// agentFunctionResponse and rendererFunctionResponse.
type FunctionResponsePayload struct {
	FunctionCallID string     `json:"functionCallId"`
	Value          any        `json:"value,omitempty"`
	Error          *CallError `json:"error,omitempty"`
}

// CallError is a function execution error {code, message}.
type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AgentFunctionResponse answers a renderer-initiated callAgentFunction.
type AgentFunctionResponse struct {
	FunctionResponsePayload
}

// payloadKey identifies the single populated message payload of an envelope.
type payloadKey string

const (
	keyCreateSurface    payloadKey = "createSurface"
	keyUpdateComponents payloadKey = "updateComponents"
	keyUpdateDataModel  payloadKey = "updateDataModel"
	keyDeleteSurface    payloadKey = "deleteSurface"
	keyCallRendererFn   payloadKey = "callRendererFunction"
	keyAgentFnResponse  payloadKey = "agentFunctionResponse"
)

var agentPayloadKeys = []payloadKey{
	keyCreateSurface, keyUpdateComponents, keyUpdateDataModel,
	keyDeleteSurface, keyCallRendererFn, keyAgentFnResponse,
}

// Envelope is one agent-to-renderer message: a protocol version plus exactly
// one payload.
type Envelope struct {
	Version string
	// Exactly one of the following is populated.
	CreateSurface         *CreateSurface
	UpdateComponents      *UpdateComponents
	UpdateDataModel       *UpdateDataModel
	DeleteSurface         *DeleteSurface
	CallRendererFunction  *CallRendererFunction
	AgentFunctionResponse *AgentFunctionResponse
	// Key records which payload was present on the wire.
	Key payloadKey
}

// UnmarshalJSON decodes and validates the exactly-one-payload rule.
func (e *Envelope) UnmarshalJSON(data []byte) error {
	if len(data) > MaxEnvelopeBytes {
		return fmt.Errorf("a2ui: envelope exceeds %d bytes", MaxEnvelopeBytes)
	}
	var probe struct {
		Version string `json:"version"`
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if probe.Version == "" {
		return fmt.Errorf("a2ui: envelope is missing the version field")
	}
	var found payloadKey
	var count int
	for _, k := range agentPayloadKeys {
		if _, ok := raw[string(k)]; ok {
			found = k
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("a2ui: envelope must contain exactly one payload key, found %d", count)
	}
	e.Version, e.Key = probe.Version, found
	decode := func(dst any) error {
		if err := json.Unmarshal(raw[string(found)], dst); err != nil {
			return fmt.Errorf("a2ui: %s payload: %w", found, err)
		}
		return nil
	}
	switch found {
	case keyCreateSurface:
		p := &CreateSurface{}
		if err := decode(p); err != nil {
			return err
		}
		e.CreateSurface = p
	case keyUpdateComponents:
		p := &UpdateComponents{}
		if err := decode(p); err != nil {
			return err
		}
		if len(p.Components) == 0 {
			return fmt.Errorf("a2ui: updateComponents requires a non-empty components list")
		}
		e.UpdateComponents = p
	case keyUpdateDataModel:
		p := &UpdateDataModel{}
		if err := decode(p); err != nil {
			return err
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(raw[string(keyUpdateDataModel)], &payload); err == nil {
			_, p.HasValue = payload["value"]
		}
		e.UpdateDataModel = p
	case keyDeleteSurface:
		p := &DeleteSurface{}
		if err := decode(p); err != nil {
			return err
		}
		e.DeleteSurface = p
	case keyCallRendererFn:
		p := &CallRendererFunction{}
		if err := decode(p); err != nil {
			return err
		}
		e.CallRendererFunction = p
	case keyAgentFnResponse:
		p := &AgentFunctionResponse{}
		if err := decode(p); err != nil {
			return err
		}
		e.AgentFunctionResponse = p
	}
	return nil
}

// MarshalJSON re-encodes the envelope with its single payload.
func (e Envelope) MarshalJSON() ([]byte, error) {
	out := map[string]any{"version": e.Version}
	switch e.Key {
	case keyCreateSurface:
		out[string(keyCreateSurface)] = e.CreateSurface
	case keyUpdateComponents:
		out[string(keyUpdateComponents)] = e.UpdateComponents
	case keyUpdateDataModel:
		out[string(keyUpdateDataModel)] = e.UpdateDataModel
	case keyDeleteSurface:
		out[string(keyDeleteSurface)] = e.DeleteSurface
	case keyCallRendererFn:
		out[string(keyCallRendererFn)] = e.CallRendererFunction
	case keyAgentFnResponse:
		out[string(keyAgentFnResponse)] = e.AgentFunctionResponse
	default:
		return nil, fmt.Errorf("a2ui: envelope has no payload")
	}
	return json.Marshal(out)
}

// Validate returns ErrUnknownVersion for versions the engine does not know.
// Unknown versions are still applied best-effort by Engine.Apply, which
// records the error alongside processing.
func (e Envelope) Validate() error {
	switch e.Version {
	case VersionV1, VersionV091, VersionV09:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrUnknownVersion, e.Version)
	}
}

// DecodeEnvelopes decodes a JSON array of envelopes (the A2A DataPart data
// payload). The input is bounded by MaxEnvelopeBytes per envelope; a single
// envelope object is also accepted.
func DecodeEnvelopes(data []byte) ([]Envelope, error) {
	if len(data) > 64*MaxEnvelopeBytes {
		return nil, fmt.Errorf("a2ui: envelope list exceeds %d bytes", 64*MaxEnvelopeBytes)
	}
	trimmed := data
	for len(trimmed) > 0 && (trimmed[0] == ' ' || trimmed[0] == '\n' || trimmed[0] == '\t' || trimmed[0] == '\r') {
		trimmed = trimmed[1:]
	}
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var one Envelope
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, err
		}
		return []Envelope{one}, nil
	}
	var list []json.RawMessage
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, err
	}
	out := make([]Envelope, 0, len(list))
	for i, raw := range list {
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("a2ui: envelope %d: %w", i, err)
		}
		out = append(out, env)
	}
	return out, nil
}

// Action is the renderer-to-agent action message body.
type Action struct {
	Name              string         `json:"name"`
	UserMessage       string         `json:"userMessage,omitempty"`
	SurfaceID         string         `json:"surfaceId"`
	SourceComponentID string         `json:"sourceComponentId"`
	Timestamp         string         `json:"timestamp"`
	Context           map[string]any `json:"context"`
}

// CallAgentFunctionMsg is the renderer-to-agent remote function call.
type CallAgentFunctionMsg struct {
	SurfaceID      string `json:"surfaceId"`
	FunctionCallID string `json:"functionCallId"`
	CallFunction   Call   `json:"callFunction"`
}

// RendererFunctionResponseMsg is the renderer's answer to an agent-initiated
// callRendererFunction.
type RendererFunctionResponseMsg struct {
	FunctionResponsePayload
}

// ErrorPayload is the renderer-to-agent error message. Exactly one of
// SurfaceID / FunctionCallID should be set (mutually exclusive per schema).
type ErrorPayload struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	SurfaceID      string `json:"surfaceId,omitempty"`
	FunctionCallID string `json:"functionCallId,omitempty"`
	Path           string `json:"path,omitempty"`
}

// RendererMessage is one renderer-to-agent message: a version plus exactly
// one of Action, CallAgentFunction, RendererFunctionResponse or Error.
type RendererMessage struct {
	Version                  string
	Action                   *Action
	CallAgentFunction        *CallAgentFunctionMsg
	RendererFunctionResponse *RendererFunctionResponseMsg
	Error                    *ErrorPayload
}

type rendererKey string

const (
	rkeyAction rendererKey = "action"
	rkeyCallFn rendererKey = "callAgentFunction"
	rkeyFnResp rendererKey = "rendererFunctionResponse"
	rkeyError  rendererKey = "error"
)

var rendererPayloadKeys = []rendererKey{rkeyAction, rkeyCallFn, rkeyFnResp, rkeyError}

// UnmarshalJSON decodes a renderer-to-agent message with exactly-one-payload
// validation.
func (m *RendererMessage) UnmarshalJSON(data []byte) error {
	if len(data) > MaxEnvelopeBytes {
		return fmt.Errorf("a2ui: message exceeds %d bytes", MaxEnvelopeBytes)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var probe struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	var found rendererKey
	count := 0
	for _, k := range rendererPayloadKeys {
		if _, ok := raw[string(k)]; ok {
			found, count = k, count+1
		}
	}
	if count != 1 {
		return fmt.Errorf("a2ui: renderer message must contain exactly one payload key, found %d", count)
	}
	m.Version = probe.Version
	decode := func(dst any) error {
		if err := json.Unmarshal(raw[string(found)], dst); err != nil {
			return fmt.Errorf("a2ui: %s payload: %w", found, err)
		}
		return nil
	}
	switch found {
	case rkeyAction:
		p := &Action{}
		if err := decode(p); err != nil {
			return err
		}
		m.Action = p
	case rkeyCallFn:
		p := &CallAgentFunctionMsg{}
		if err := decode(p); err != nil {
			return err
		}
		m.CallAgentFunction = p
	case rkeyFnResp:
		p := &RendererFunctionResponseMsg{}
		if err := decode(p); err != nil {
			return err
		}
		m.RendererFunctionResponse = p
	case rkeyError:
		p := &ErrorPayload{}
		if err := decode(p); err != nil {
			return err
		}
		m.Error = p
	}
	return nil
}

// MarshalJSON re-encodes the renderer-to-agent message.
func (m RendererMessage) MarshalJSON() ([]byte, error) {
	out := map[string]any{"version": m.Version}
	switch {
	case m.Action != nil:
		out[string(rkeyAction)] = m.Action
	case m.CallAgentFunction != nil:
		out[string(rkeyCallFn)] = m.CallAgentFunction
	case m.RendererFunctionResponse != nil:
		out[string(rkeyFnResp)] = m.RendererFunctionResponse
	case m.Error != nil:
		out[string(rkeyError)] = m.Error
	default:
		return nil, fmt.Errorf("a2ui: renderer message has no payload")
	}
	return json.Marshal(out)
}

// NewActionMessage builds an action envelope for a button press with the
// context values already resolved, timestamped now (ISO 8601 UTC).
func NewActionMessage(version, surfaceID, sourceComponentID, name string, context map[string]any) RendererMessage {
	if context == nil {
		context = map[string]any{}
	}
	return RendererMessage{
		Version: version,
		Action: &Action{
			Name:              name,
			SurfaceID:         surfaceID,
			SourceComponentID: sourceComponentID,
			Timestamp:         time.Now().UTC().Format(time.RFC3339),
			Context:           context,
		},
	}
}
