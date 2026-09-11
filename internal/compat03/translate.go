package compat03

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// dataPartCompatFlag mirrors the flag the official SDK's compatibility
// layer uses: 0.3 data parts must be JSON objects, so non-object Data
// values travel wrapped in {"value": …} and the flag marks the wrap.
const dataPartCompatFlag = "data_part_compat"

// stateFromWire maps a 0.3 task state to the 1.x constant. Unknown or
// missing states map to TaskStateUnspecified (never an error — hostile
// strings must not break the stream).
func stateFromWire(s string) a2a.TaskState {
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
	default: // "unknown", "", typos
		return a2a.TaskStateUnspecified
	}
}

// stateToWire maps a 1.x task state back to its 0.3 spelling. The
// unspecified state travels as "unknown" (0.3 requires a state string).
func stateToWire(s a2a.TaskState) string {
	switch s {
	case a2a.TaskStateSubmitted:
		return "submitted"
	case a2a.TaskStateWorking:
		return "working"
	case a2a.TaskStateInputRequired:
		return "input-required"
	case a2a.TaskStateCompleted:
		return "completed"
	case a2a.TaskStateFailed:
		return "failed"
	case a2a.TaskStateCanceled:
		return "canceled"
	case a2a.TaskStateRejected:
		return "rejected"
	case a2a.TaskStateAuthRequired:
		return "auth-required"
	default:
		return "unknown"
	}
}

// roleFromWire maps a 0.3 role ("user"/"agent") to the 1.x constant.
func roleFromWire(r string) a2a.MessageRole {
	switch r {
	case "user":
		return a2a.MessageRoleUser
	case "agent":
		return a2a.MessageRoleAgent
	default:
		return a2a.MessageRoleUnspecified
	}
}

// roleToWire maps a 1.x role to its 0.3 spelling. The unspecified role
// can not travel (0.3 has only user/agent); "user" is the safe default
// since only the client sends outbound messages.
func roleToWire(r a2a.MessageRole) string {
	if r == a2a.MessageRoleAgent {
		return "agent"
	}
	return "user"
}

// timestampLayouts are the ISO 8601 spellings seen in the wild. The 0.3
// spec's own examples omit the zone offset, so a zoneless layout is
// included (parsed as UTC).
var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
}

// parseTimestamp parses a 0.3 status timestamp, returning nil when
// unparseable or empty (the field is optional).
func parseTimestamp(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, layout := range timestampLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// formatTimestamp renders a 1.x timestamp for the wire; nil stays empty.
func formatTimestamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// partFromWire translates one 0.3 part into the SDK union. Unknown part
// kinds and malformed file objects degrade to Data parts carrying the
// raw JSON instead of failing.
func partFromWire(w wirePart) *a2a.Part {
	switch w.Kind {
	case "text":
		return &a2a.Part{Content: a2a.Text(w.Text), Metadata: w.Metadata}
	case "file":
		return filePartFromWire(w)
	case "data":
		return dataPartFromWire(w)
	default:
		return rawFallbackPart(w.Raw, w.Kind)
	}
}

// filePartFromWire maps a 0.3 file part: bytes (or the spec example's
// "data" spelling) become Raw content, uri becomes URL content.
func filePartFromWire(w wirePart) *a2a.Part {
	f := w.File
	if f == nil {
		return rawFallbackPart(w.Raw, w.Kind)
	}
	switch {
	case f.Bytes != "", f.Data != "":
		encoded := f.Bytes
		if encoded == "" {
			encoded = f.Data
		}
		content, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			// Some agents ship unencoded payloads; keep the bytes rather
			// than dropping the part (mirrors the SDK compat layer).
			content = []byte(encoded)
		}
		return &a2a.Part{
			Content:   a2a.Raw(content),
			Metadata:  w.Metadata,
			MediaType: f.MimeType,
			Filename:  f.Name,
		}
	case f.URI != "":
		return &a2a.Part{
			Content:   a2a.URL(f.URI),
			Metadata:  w.Metadata,
			MediaType: f.MimeType,
			Filename:  f.Name,
		}
	default:
		return rawFallbackPart(w.Raw, w.Kind)
	}
}

// dataPartFromWire maps a 0.3 data part, unwrapping the compatibility
// envelope when present (see dataPartCompatFlag).
func dataPartFromWire(w wirePart) *a2a.Part {
	var value any
	if len(w.Data) > 0 {
		if err := json.Unmarshal(w.Data, &value); err != nil {
			value = map[string]any{"raw": string(w.Data)}
		}
	}
	if compat, ok := w.Metadata[dataPartCompatFlag].(bool); ok && compat {
		if m, ok := value.(map[string]any); ok && len(m) == 1 {
			if v, ok := m["value"]; ok {
				value = v
			}
		}
	}
	return &a2a.Part{Content: a2a.Data{Value: value}, Metadata: w.Metadata}
}

// rawFallbackPart preserves an untranslatable part as structured data.
func rawFallbackPart(raw json.RawMessage, kind string) *a2a.Part {
	var value any = map[string]any{"kind": kind}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &value); err != nil {
			value = map[string]any{"kind": kind, "raw": string(raw)}
		}
	}
	return &a2a.Part{Content: a2a.Data{Value: value}}
}

// partToWire translates an SDK part into the 0.3 union.
func partToWire(p *a2a.Part) wirePart {
	if p == nil {
		return wirePart{Kind: "text"}
	}
	switch c := p.Content.(type) {
	case a2a.Text:
		return wirePart{Kind: "text", Text: string(c), Metadata: p.Metadata}
	case a2a.Raw:
		return wirePart{
			Kind: "file",
			File: &wireFile{
				Name:     p.Filename,
				MimeType: p.MediaType,
				Bytes:    base64.StdEncoding.EncodeToString(c),
			},
			Metadata: p.Metadata,
		}
	case a2a.URL:
		return wirePart{
			Kind: "file",
			File: &wireFile{
				Name:     p.Filename,
				MimeType: p.MediaType,
				URI:      string(c),
			},
			Metadata: p.Metadata,
		}
	case a2a.Data:
		value, metadata := wrapDataValue(c.Value, p.Metadata)
		raw, err := json.Marshal(value)
		if err != nil {
			raw = []byte(`{}`)
		}
		return wirePart{Kind: "data", Data: raw, Metadata: metadata}
	default: // nil or unknown content: degrade to empty text
		return wirePart{Kind: "text", Metadata: p.Metadata}
	}
}

// wrapDataValue ensures a 0.3 data payload is a JSON object, wrapping
// non-object values per the SDK compatibility convention.
func wrapDataValue(v any, metadata map[string]any) (any, map[string]any) {
	if _, ok := v.(map[string]any); ok || v == nil {
		if v == nil {
			v = map[string]any{}
		}
		return v, metadata
	}
	metadata = maps.Clone(metadata)
	if metadata == nil {
		metadata = make(map[string]any, 1)
	}
	metadata[dataPartCompatFlag] = true
	return map[string]any{"value": v}, metadata
}

// partsFromWire translates capped part lists.
func partsFromWire(ws []wirePart) a2a.ContentParts {
	if len(ws) == 0 {
		return nil
	}
	limit := min(len(ws), maxPartsPerMessage)
	out := make(a2a.ContentParts, 0, limit)
	for i := range ws {
		if len(out) >= limit {
			break
		}
		if p := partFromWire(ws[i]); p != nil {
			out = append(out, p)
		}
	}
	return out
}

// partsToWire translates part lists, skipping nil entries.
func partsToWire(ps a2a.ContentParts) []wirePart {
	if len(ps) == 0 {
		return nil
	}
	out := make([]wirePart, 0, len(ps))
	for _, p := range ps {
		out = append(out, partToWire(p))
	}
	return out
}

// messageFromWire translates a 0.3 Message.
func messageFromWire(w *wireMessage) *a2a.Message {
	if w == nil {
		return nil
	}
	return &a2a.Message{
		ID:             w.MessageID,
		ContextID:      w.ContextID,
		Extensions:     w.Extensions,
		Metadata:       w.Metadata,
		Parts:          partsFromWire(w.Parts),
		ReferenceTasks: taskIDsFromWire(w.ReferenceTasks),
		Role:           roleFromWire(w.Role),
		TaskID:         a2a.TaskID(w.TaskID),
	}
}

// messageToWire translates an SDK Message. The caller is responsible for
// ensuring Message.ID is set (fresh message ids are generated in
// sendParams).
func messageToWire(m *a2a.Message) wireMessage {
	if m == nil {
		return wireMessage{}
	}
	return wireMessage{
		MessageID:      m.ID,
		TaskID:         string(m.TaskID),
		ContextID:      m.ContextID,
		Role:           roleToWire(m.Role),
		Parts:          partsToWire(m.Parts),
		Metadata:       m.Metadata,
		Extensions:     m.Extensions,
		ReferenceTasks: taskIDsToWire(m.ReferenceTasks),
	}
}

func taskIDsFromWire(ids []string) []a2a.TaskID {
	if len(ids) == 0 {
		return nil
	}
	ids = capStrings(ids, maxPartsPerMessage)
	out := make([]a2a.TaskID, len(ids))
	for i, id := range ids {
		out[i] = a2a.TaskID(id)
	}
	return out
}

func taskIDsToWire(ids []a2a.TaskID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

// statusFromWire translates a 0.3 status block.
func statusFromWire(w wireStatus) a2a.TaskStatus {
	return a2a.TaskStatus{
		State:     stateFromWire(w.State),
		Message:   messageFromWire(w.Message),
		Timestamp: parseTimestamp(w.Timestamp),
	}
}

// statusToWire translates a status block for the wire.
func statusToWire(s a2a.TaskStatus) wireStatus {
	w := wireStatus{
		State:     stateToWire(s.State),
		Timestamp: formatTimestamp(s.Timestamp),
	}
	if s.Message != nil {
		w.Message = ptr(messageToWire(s.Message))
	}
	return w
}

// artifactFromWire translates a 0.3 Artifact.
func artifactFromWire(w *wireArtifact) *a2a.Artifact {
	if w == nil {
		return nil
	}
	return &a2a.Artifact{
		ID:          a2a.ArtifactID(w.ArtifactID),
		Description: w.Description,
		Extensions:  w.Extensions,
		Metadata:    w.Metadata,
		Name:        w.Name,
		Parts:       partsFromWire(w.Parts),
	}
}

// artifactToWire translates an SDK Artifact.
func artifactToWire(a *a2a.Artifact) wireArtifact {
	if a == nil {
		return wireArtifact{}
	}
	return wireArtifact{
		ArtifactID:  string(a.ID),
		Name:        a.Name,
		Description: a.Description,
		Parts:       partsToWire(a.Parts),
		Metadata:    a.Metadata,
		Extensions:  a.Extensions,
	}
}

// taskFromWire translates a 0.3 Task (also the "kind":"task" stream
// event).
func taskFromWire(w *wireTask) *a2a.Task {
	if w == nil {
		return nil
	}
	t := &a2a.Task{
		ID:        a2a.TaskID(w.ID),
		ContextID: w.ContextID,
		Status:    statusFromWire(w.Status),
		Metadata:  w.Metadata,
	}
	if len(w.History) > 0 {
		limit := min(len(w.History), maxHistoryMessages)
		t.History = make([]*a2a.Message, 0, limit)
		for i := range w.History {
			if len(t.History) >= limit {
				break
			}
			if m := messageFromWire(&w.History[i]); m != nil {
				t.History = append(t.History, m)
			}
		}
	}
	if len(w.Artifacts) > 0 {
		limit := min(len(w.Artifacts), maxArtifactsPerTask)
		t.Artifacts = make([]*a2a.Artifact, 0, limit)
		for i := range w.Artifacts {
			if len(t.Artifacts) >= limit {
				break
			}
			if a := artifactFromWire(&w.Artifacts[i]); a != nil {
				t.Artifacts = append(t.Artifacts, a)
			}
		}
	}
	return t
}

// taskToWire translates an SDK Task.
func taskToWire(t *a2a.Task) wireTask {
	if t == nil {
		return wireTask{}
	}
	w := wireTask{
		ID:        string(t.ID),
		ContextID: t.ContextID,
		Status:    statusToWire(t.Status),
		Metadata:  t.Metadata,
	}
	if len(t.History) > 0 {
		w.History = make([]wireMessage, 0, len(t.History))
		for _, m := range t.History {
			if m == nil {
				continue
			}
			w.History = append(w.History, messageToWire(m))
		}
	}
	if len(t.Artifacts) > 0 {
		w.Artifacts = make([]wireArtifact, 0, len(t.Artifacts))
		for _, a := range t.Artifacts {
			if a == nil {
				continue
			}
			w.Artifacts = append(w.Artifacts, artifactToWire(a))
		}
	}
	return w
}

// statusUpdateFromWire translates a "kind":"status-update" event.
func statusUpdateFromWire(w *wireStatusUpdate) *a2a.TaskStatusUpdateEvent {
	if w == nil {
		return nil
	}
	return &a2a.TaskStatusUpdateEvent{
		ContextID: w.ContextID,
		Status:    statusFromWire(w.Status),
		TaskID:    a2a.TaskID(w.TaskID),
		Metadata:  w.Metadata,
	}
}

// statusUpdateToWire translates a status-update event, setting final per
// the 0.3 convention the SDK's compat layer uses (terminal states and
// input-required end the interaction).
func statusUpdateToWire(e *a2a.TaskStatusUpdateEvent) wireStatusUpdate {
	if e == nil {
		return wireStatusUpdate{}
	}
	final := e.Status.State.Terminal() || e.Status.State == a2a.TaskStateInputRequired
	return wireStatusUpdate{
		TaskID:    string(e.TaskID),
		ContextID: e.ContextID,
		Kind:      "status-update",
		Status:    statusToWire(e.Status),
		Final:     final,
		Metadata:  e.Metadata,
	}
}

// artifactUpdateFromWire translates a "kind":"artifact-update" event.
func artifactUpdateFromWire(w *wireArtifactUpdate) *a2a.TaskArtifactUpdateEvent {
	if w == nil {
		return nil
	}
	return &a2a.TaskArtifactUpdateEvent{
		Append:    w.Append,
		Artifact:  artifactFromWire(&w.Artifact),
		ContextID: w.ContextID,
		LastChunk: w.LastChunk,
		TaskID:    a2a.TaskID(w.TaskID),
		Metadata:  w.Metadata,
	}
}

// artifactUpdateToWire translates an artifact-update event.
func artifactUpdateToWire(e *a2a.TaskArtifactUpdateEvent) wireArtifactUpdate {
	if e == nil {
		return wireArtifactUpdate{}
	}
	return wireArtifactUpdate{
		TaskID:    string(e.TaskID),
		ContextID: e.ContextID,
		Kind:      "artifact-update",
		Artifact:  artifactToWire(e.Artifact),
		Append:    e.Append,
		LastChunk: e.LastChunk,
		Metadata:  e.Metadata,
	}
}

// eventFromResult decodes one raw stream/send result payload into an SDK
// event. The boolean reports the 0.3 "final" flag (status-update only);
// callers end the stream when it is set.
func eventFromResult(raw json.RawMessage) (a2a.Event, bool, error) {
	kind := resultKind(raw)
	switch kind {
	case "task":
		var w wireTask
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, false, fmt.Errorf("compat03: decode task result: %w", err)
		}
		return taskFromWire(&w), false, nil
	case "status-update":
		var w wireStatusUpdate
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, false, fmt.Errorf("compat03: decode status-update: %w", err)
		}
		return statusUpdateFromWire(&w), w.Final, nil
	case "artifact-update":
		var w wireArtifactUpdate
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, false, fmt.Errorf("compat03: decode artifact-update: %w", err)
		}
		return artifactUpdateFromWire(&w), false, nil
	case "message":
		var w wireMessage
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, false, fmt.Errorf("compat03: decode message result: %w", err)
		}
		return messageFromWire(&w), false, nil
	default:
		return nil, false, fmt.Errorf("compat03: unrecognized 0.3 result payload (kind %q)", kind)
	}
}

// pushFromWire translates a TaskPushNotificationConfig (set params / get
// result / list element). The 0.3 schemes list collapses to the first
// scheme, mirroring the SDK's compatibility mapping.
func pushFromWire(w wireTaskPushConfig) *a2a.PushConfig {
	cfg := &a2a.PushConfig{
		TaskID: a2a.TaskID(w.TaskID),
		ID:     w.Config.ID,
		Token:  w.Config.Token,
		URL:    w.Config.URL,
	}
	if w.Config.Auth != nil {
		auth := &a2a.PushAuthInfo{Credentials: w.Config.Auth.Credentials}
		if len(w.Config.Auth.Schemes) > 0 {
			auth.Scheme = w.Config.Auth.Schemes[0]
		}
		cfg.Auth = auth
	}
	return cfg
}

// pushToWire translates an SDK push config into the set-method params.
func pushToWire(p *a2a.PushConfig) wireTaskPushConfig {
	if p == nil {
		return wireTaskPushConfig{}
	}
	w := wireTaskPushConfig{
		TaskID: string(p.TaskID),
		Config: wirePushConfig{
			ID:    p.ID,
			URL:   p.URL,
			Token: p.Token,
		},
	}
	if p.Auth != nil {
		schemes := []string{}
		if p.Auth.Scheme != "" {
			schemes = []string{p.Auth.Scheme}
		}
		w.Config.Auth = &wirePushAuth{
			Schemes:     schemes,
			Credentials: p.Auth.Credentials,
		}
	}
	return w
}

// pushConfigToWire translates just the config block (the send-message
// configuration embeds a PushConfig without the task id).
func pushConfigToWire(p *a2a.PushConfig) *wirePushConfig {
	if p == nil {
		return nil
	}
	w := pushToWire(p)
	return &w.Config
}

// ptr is a small generic address-of helper.
func ptr[T any](v T) *T { return &v }

// sendParamsFromRequest builds the message/send and message/stream
// params. Fresh message ids (and any ids the request lacked) come from
// newID. ReturnImmediately inverts to the 0.3 blocking flag.
func sendParamsFromRequest(req *a2a.SendMessageRequest, newID func() string) (wireSendParams, error) {
	if req == nil || req.Message == nil {
		return wireSendParams{}, fmt.Errorf("compat03: send request without a message")
	}
	params := wireSendParams{Message: messageToWire(req.Message)}
	if params.Message.MessageID == "" {
		params.Message.MessageID = newID()
	}
	params.Metadata = req.Metadata
	if req.Config != nil {
		blocking := !req.Config.ReturnImmediately
		params.Config = &wireSendConfiguration{
			AcceptedOutputModes: req.Config.AcceptedOutputModes,
			HistoryLength:       req.Config.HistoryLength,
			PushConfig:          pushConfigToWire(req.Config.PushConfig),
			Blocking:            &blocking,
		}
	}
	return params, nil
}
