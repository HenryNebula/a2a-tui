// Push body decoding. Agents POST one stream event per request in one of
// two flavors:
//
//   - A2A 1.0: the StreamResponse wrapper — exactly one of {"task"…},
//     {"message"…}, {"statusUpdate"…}, {"artifactUpdate"…}. Decoded
//     directly by the SDK's a2a.StreamResponse.
//   - A2A 0.3: the bare legacy event — a top-level "kind" of
//     "status-update" | "artifact-update" | "message" | "task" with
//     camelCase fields and {kind:…} parts. The full 0.3 client lives in
//     internal/compat03; its translation helpers are unexported (and that
//     package is frozen), so the push-relevant subset is decoded here.
package pushsrv

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// maxPartsPerMessage bounds hostile part arrays (mirrors compat03).
const maxPartsPerMessage = 1024

// DecodeEvent decodes one push webhook body into an SDK event. Flavor
// probing: a top-level "kind" member means the 0.3 event; otherwise the
// body must be a 1.0 StreamResponse. Bodies that decode as neither are
// reported as errors (the agent answers 400 back from the webhook).
func DecodeEvent(body []byte) (a2a.Event, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errors.New("pushsrv: empty body")
	}
	var probe struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(trimmed, &probe) // probe only; shape errors surface below
	if probe.Kind != "" {
		return decodeV03(trimmed)
	}

	var sr a2a.StreamResponse
	if err := json.Unmarshal(trimmed, &sr); err != nil {
		// Some 0.3 agents omit the kind member; sniff before failing.
		if sniffV03(trimmed) != "" {
			return decodeV03(trimmed)
		}
		return nil, fmt.Errorf("pushsrv: decode body: %w", err)
	}
	if sr.Event == nil {
		// No 1.0 wrapper member matched; try the legacy shape before
		// giving up so kind-less 0.3 pushes still land.
		if sniffV03(trimmed) != "" {
			return decodeV03(trimmed)
		}
		return nil, errors.New("pushsrv: body carried no task, message, statusUpdate or artifactUpdate")
	}
	return sr.Event, nil
}

// sniffV03 guesses whether raw is a kind-less 0.3 event from its field
// shapes (the same heuristics compat03's client uses for legacy servers),
// returning the guessed kind or "".
func sniffV03(raw []byte) string {
	var probe struct {
		ArtifactID string          `json:"artifactId"`
		Artifact   json.RawMessage `json:"artifact"`
		TaskID     string          `json:"taskId"`
		Status     json.RawMessage `json:"status"`
		ID         string          `json:"id"`
		MessageID  string          `json:"messageId"`
		Parts      json.RawMessage `json:"parts"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
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
	}
	return ""
}

// decodeV03 decodes one legacy 0.3 event body (with or without "kind").
func decodeV03(raw []byte) (a2a.Event, error) {
	kind := sniffV03(raw)
	var probe struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &probe)
	if probe.Kind != "" {
		kind = probe.Kind
	}
	switch kind {
	case "status-update":
		var w v03StatusUpdate
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pushsrv: decode 0.3 status-update: %w", err)
		}
		return w.event(), nil
	case "artifact-update":
		var w v03ArtifactUpdate
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pushsrv: decode 0.3 artifact-update: %w", err)
		}
		return w.event(), nil
	case "message":
		var w v03Message
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pushsrv: decode 0.3 message: %w", err)
		}
		return w.event(), nil
	case "task":
		var w v03Task
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, fmt.Errorf("pushsrv: decode 0.3 task: %w", err)
		}
		return w.event(), nil
	default:
		return nil, fmt.Errorf("pushsrv: unrecognized 0.3 push payload (kind %q)", kind)
	}
}

// ---------------------------------------------------------------------------
// 0.3 wire subset → SDK translation
// ---------------------------------------------------------------------------

// v03Part is one {kind: text|file|data} content part.
type v03Part struct {
	Kind string          `json:"kind"`
	Text string          `json:"text,omitempty"`
	File *v03File        `json:"file,omitempty"`
	Data json.RawMessage `json:"data,omitempty"`
}

// v03File is the nested file object (base64 bytes or a uri).
type v03File struct {
	Name     string `json:"name,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Bytes    string `json:"bytes,omitempty"`
	URI      string `json:"uri,omitempty"`
}

// v03Message is the 0.3 Message.
type v03Message struct {
	MessageID string    `json:"messageId"`
	TaskID    string    `json:"taskId,omitempty"`
	ContextID string    `json:"contextId,omitempty"`
	Role      string    `json:"role,omitempty"`
	Parts     []v03Part `json:"parts,omitempty"`
}

// v03Status is the status block of tasks and status-updates.
type v03Status struct {
	State     string      `json:"state"`
	Message   *v03Message `json:"message,omitempty"`
	Timestamp string      `json:"timestamp,omitempty"`
}

// v03Artifact is the 0.3 Artifact.
type v03Artifact struct {
	ArtifactID  string    `json:"artifactId"`
	Name        string    `json:"name,omitempty"`
	Description string    `json:"description,omitempty"`
	Parts       []v03Part `json:"parts,omitempty"`
}

// v03StatusUpdate is the "kind":"status-update" event.
type v03StatusUpdate struct {
	TaskID    string    `json:"taskId"`
	ContextID string    `json:"contextId"`
	Status    v03Status `json:"status"`
}

// v03ArtifactUpdate is the "kind":"artifact-update" event.
type v03ArtifactUpdate struct {
	TaskID    string      `json:"taskId"`
	ContextID string      `json:"contextId"`
	Artifact  v03Artifact `json:"artifact"`
	Append    bool        `json:"append,omitempty"`
	LastChunk bool        `json:"lastChunk,omitempty"`
}

// v03Task is the 0.3 Task (also the "kind":"task" event).
type v03Task struct {
	ID        string        `json:"id"`
	ContextID string        `json:"contextId"`
	Status    v03Status     `json:"status"`
	Artifacts []v03Artifact `json:"artifacts,omitempty"`
}

func (w *v03StatusUpdate) event() *a2a.TaskStatusUpdateEvent {
	return &a2a.TaskStatusUpdateEvent{
		TaskID:    a2a.TaskID(w.TaskID),
		ContextID: w.ContextID,
		Status:    w.Status.status(),
	}
}

func (w *v03ArtifactUpdate) event() *a2a.TaskArtifactUpdateEvent {
	return &a2a.TaskArtifactUpdateEvent{
		TaskID:    a2a.TaskID(w.TaskID),
		ContextID: w.ContextID,
		Artifact:  w.Artifact.artifact(),
		Append:    w.Append,
		LastChunk: w.LastChunk,
	}
}

func (w *v03Message) event() *a2a.Message {
	return &a2a.Message{
		ID:        w.MessageID,
		TaskID:    a2a.TaskID(w.TaskID),
		ContextID: w.ContextID,
		Role:      v03Role(w.Role),
		Parts:     v03Parts(w.Parts),
	}
}

func (w *v03Task) event() *a2a.Task {
	t := &a2a.Task{
		ID:        a2a.TaskID(w.ID),
		ContextID: w.ContextID,
		Status:    w.Status.status(),
	}
	for i := range w.Artifacts {
		if a := w.Artifacts[i].artifact(); a != nil {
			t.Artifacts = append(t.Artifacts, a)
		}
	}
	return t
}

func (w *v03Status) status() a2a.TaskStatus {
	return a2a.TaskStatus{
		State:   v03State(w.State),
		Message: w.Message.eventOrNil(),
	}
}

func (w *v03Artifact) artifact() *a2a.Artifact {
	return &a2a.Artifact{
		ID:          a2a.ArtifactID(w.ArtifactID),
		Name:        w.Name,
		Description: w.Description,
		Parts:       v03Parts(w.Parts),
	}
}

func (w *v03Message) eventOrNil() *a2a.Message {
	if w == nil {
		return nil
	}
	return w.event()
}

// v03State maps a 0.3 state string to the 1.x constant (unknown →
// unspecified; hostile strings never break decoding).
func v03State(s string) a2a.TaskState {
	switch s {
	case "submitted":
		return a2a.TaskStateSubmitted
	case "working":
		return a2a.TaskStateWorking
	case "input-required":
		return a2a.TaskStateInputRequired
	case "completed":
		return a2a.TaskStateCompleted
	case "failed":
		return a2a.TaskStateFailed
	case "canceled":
		return a2a.TaskStateCanceled
	case "rejected":
		return a2a.TaskStateRejected
	case "auth-required":
		return a2a.TaskStateAuthRequired
	default:
		return a2a.TaskStateUnspecified
	}
}

// v03Role maps a 0.3 role to the 1.x constant.
func v03Role(r string) a2a.MessageRole {
	switch r {
	case "user":
		return a2a.MessageRoleUser
	case "agent":
		return a2a.MessageRoleAgent
	default:
		return a2a.MessageRoleUnspecified
	}
}

// v03Parts translates a capped part list.
func v03Parts(in []v03Part) a2a.ContentParts {
	if len(in) == 0 {
		return nil
	}
	limit := min(len(in), maxPartsPerMessage)
	out := make(a2a.ContentParts, 0, limit)
	for i := range in {
		if len(out) >= limit {
			break
		}
		switch in[i].Kind {
		case "text":
			out = append(out, &a2a.Part{Content: a2a.Text(in[i].Text)})
		case "file":
			out = append(out, v03FilePart(&in[i]))
		case "data":
			out = append(out, v03DataPart(&in[i]))
		default:
			out = append(out, &a2a.Part{Content: a2a.Data{Value: map[string]any{"kind": in[i].Kind}}})
		}
	}
	return out
}

// v03FilePart translates a file part: base64 bytes become Raw content
// (unencoded payloads are kept verbatim, mirroring compat03), uri becomes
// URL content.
func v03FilePart(w *v03Part) *a2a.Part {
	f := w.File
	if f == nil {
		return &a2a.Part{Content: a2a.Data{Value: map[string]any{"kind": "file"}}}
	}
	if f.Bytes != "" {
		content, err := base64.StdEncoding.DecodeString(f.Bytes)
		if err != nil {
			content = []byte(f.Bytes)
		}
		return &a2a.Part{
			Content:   a2a.Raw(content),
			MediaType: f.MimeType,
			Filename:  f.Name,
		}
	}
	if f.URI != "" {
		return &a2a.Part{
			Content:   a2a.URL(f.URI),
			MediaType: f.MimeType,
			Filename:  f.Name,
		}
	}
	return &a2a.Part{Content: a2a.Data{Value: map[string]any{"kind": "file"}}}
}

// v03DataPart translates a data part, tolerating non-object payloads.
func v03DataPart(w *v03Part) *a2a.Part {
	var value any
	if len(w.Data) > 0 {
		if err := json.Unmarshal(w.Data, &value); err != nil {
			value = map[string]any{"raw": string(w.Data)}
		}
	}
	return &a2a.Part{Content: a2a.Data{Value: value}}
}
