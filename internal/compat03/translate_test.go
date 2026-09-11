package compat03

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// loadFixture reads a testdata file.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}

// resultOf extracts the raw result member of a JSON-RPC envelope.
func resultOf(t *testing.T, envelope []byte) json.RawMessage {
	t.Helper()
	var env rpcResponse
	if err := json.Unmarshal(envelope, &env); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return env.Result
}

// canon re-encodes v through the JSON round-trip so struct/field-order
// differences never matter.
func canon(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

func TestStateRoundTrip(t *testing.T) {
	cases := []struct {
		wire string
		sdk  a2a.TaskState
	}{
		{"submitted", a2a.TaskStateSubmitted},
		{"working", a2a.TaskStateWorking},
		{"input-required", a2a.TaskStateInputRequired},
		{"completed", a2a.TaskStateCompleted},
		{"failed", a2a.TaskStateFailed},
		{"canceled", a2a.TaskStateCanceled},
		{"rejected", a2a.TaskStateRejected},
		{"auth-required", a2a.TaskStateAuthRequired},
	}
	for _, c := range cases {
		if got := stateFromWire(c.wire); got != c.sdk {
			t.Errorf("stateFromWire(%q) = %v, want %v", c.wire, got, c.sdk)
		}
		if got := stateToWire(c.sdk); got != c.wire {
			t.Errorf("stateToWire(%v) = %q, want %q", c.sdk, got, c.wire)
		}
	}
	// Unknown spellings degrade to unspecified and back to "unknown".
	for _, hostile := range []string{"", "unknown", "COMPLETED", "finished", "\x00"} {
		if got := stateFromWire(hostile); got != a2a.TaskStateUnspecified {
			t.Errorf("stateFromWire(%q) = %v, want unspecified", hostile, got)
		}
	}
	if got := stateToWire(a2a.TaskStateUnspecified); got != "unknown" {
		t.Errorf("stateToWire(unspecified) = %q, want unknown", got)
	}
}

func TestRoleRoundTrip(t *testing.T) {
	if roleFromWire("user") != a2a.MessageRoleUser || roleFromWire("agent") != a2a.MessageRoleAgent {
		t.Fatal("role mapping broken")
	}
	if roleFromWire("AGENT") != a2a.MessageRoleUnspecified || roleFromWire("") != a2a.MessageRoleUnspecified {
		t.Fatal("hostile roles must degrade to unspecified")
	}
	if roleToWire(a2a.MessageRoleAgent) != "agent" || roleToWire(a2a.MessageRoleUser) != "user" {
		t.Fatal("roleToWire broken")
	}
}

func TestTimestamps(t *testing.T) {
	// Zoned, zoneless (spec example spelling), and space-separated.
	for _, s := range []string{
		"2025-04-02T16:59:35.331844Z",
		"2025-04-02T16:59:25.331844",
		"2025-04-02T16:59:25",
		"2025-04-02 16:59:25",
	} {
		if ts := parseTimestamp(s); ts == nil {
			t.Errorf("parseTimestamp(%q) = nil, want a time", s)
		}
	}
	if ts := parseTimestamp(""); ts != nil {
		t.Error("empty timestamp should parse to nil")
	}
	if ts := parseTimestamp("not a date"); ts != nil {
		t.Error("garbage timestamp should parse to nil")
	}
	want := time.Date(2025, 4, 2, 16, 59, 35, 331844000, time.UTC)
	got := parseTimestamp("2025-04-02T16:59:35.331844Z")
	if got == nil || !got.Equal(want) {
		t.Errorf("zoned timestamp = %v, want %v", got, want)
	}
	if s := formatTimestamp(&want); s != "2025-04-02T16:59:35.331844Z" {
		t.Errorf("formatTimestamp = %q", s)
	}
	if s := formatTimestamp(nil); s != "" {
		t.Errorf("formatTimestamp(nil) = %q", s)
	}
}

func TestPartFromWireTable(t *testing.T) {
	cases := []struct {
		name  string
		raw   string
		check func(t *testing.T, p *a2a.Part)
	}{
		{
			name: "text",
			raw:  `{"kind":"text","text":"hi"}`,
			check: func(t *testing.T, p *a2a.Part) {
				if p.Text() != "hi" {
					t.Errorf("text = %q", p.Text())
				}
			},
		},
		{
			name: "file bytes",
			raw:  `{"kind":"file","file":{"bytes":"cG9uZw==","mimeType":"text/plain","name":"a.txt"}}`,
			check: func(t *testing.T, p *a2a.Part) {
				if string(p.Raw()) != "pong" || p.MediaType != "text/plain" || p.Filename != "a.txt" {
					t.Errorf("raw part = %+v", p)
				}
			},
		},
		{
			name: "file data (spec example quirk)",
			raw:  `{"kind":"file","file":{"data":"cG9uZw==","mimeType":"image/png"}}`,
			check: func(t *testing.T, p *a2a.Part) {
				if string(p.Raw()) != "pong" || p.MediaType != "image/png" {
					t.Errorf("raw part = %+v", p)
				}
			},
		},
		{
			name: "file bytes not base64 keeps raw string bytes",
			raw:  `{"kind":"file","file":{"bytes":"%%not-b64%%"}}`,
			check: func(t *testing.T, p *a2a.Part) {
				if string(p.Raw()) != "%%not-b64%%" {
					t.Errorf("raw part = %q", p.Raw())
				}
			},
		},
		{
			name: "file uri",
			raw:  `{"kind":"file","file":{"uri":"https://example.com/x.png","mimeType":"image/png","name":"x.png"}}`,
			check: func(t *testing.T, p *a2a.Part) {
				if string(p.URL()) != "https://example.com/x.png" || p.Filename != "x.png" {
					t.Errorf("url part = %+v", p)
				}
			},
		},
		{
			name: "data object",
			raw:  `{"kind":"data","data":{"a":1}}`,
			check: func(t *testing.T, p *a2a.Part) {
				m, ok := p.Data().(map[string]any)
				if !ok || m["a"] != float64(1) {
					t.Errorf("data part = %#v", p.Data())
				}
			},
		},
		{
			name: "data non-object survives",
			raw:  `{"kind":"data","data":[1,2]}`,
			check: func(t *testing.T, p *a2a.Part) {
				if _, ok := p.Data().([]any); !ok {
					t.Errorf("data part = %#v", p.Data())
				}
			},
		},
		{
			name: "file part without file object degrades to data",
			raw:  `{"kind":"file"}`,
			check: func(t *testing.T, p *a2a.Part) {
				if _, ok := p.Data().(map[string]any); !ok {
					t.Errorf("fallback part = %#v", p.Data())
				}
			},
		},
		{
			name: "unknown kind preserved as data",
			raw:  `{"kind":"quux","payload":{"x":1}}`,
			check: func(t *testing.T, p *a2a.Part) {
				m, ok := p.Data().(map[string]any)
				if !ok {
					t.Fatalf("fallback part = %#v", p.Data())
				}
				if _, present := m["payload"]; !present {
					t.Errorf("unknown kind lost payload: %#v", m)
				}
			},
		},
		{
			name: "metadata carried",
			raw:  `{"kind":"text","text":"hi","metadata":{"k":"v"}}`,
			check: func(t *testing.T, p *a2a.Part) {
				if p.Metadata["k"] != "v" {
					t.Errorf("metadata = %#v", p.Metadata)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var w wirePart
			if err := json.Unmarshal([]byte(tc.raw), &w); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			p := partFromWire(w)
			if p == nil {
				t.Fatal("nil part")
			}
			tc.check(t, p)
		})
	}
}

func TestPartToWireAndBack(t *testing.T) {
	parts := []*a2a.Part{
		a2a.NewTextPart("hello"),
		{Content: a2a.Raw([]byte{0, 1, 2}), MediaType: "application/octet-stream", Filename: "bin"},
		a2a.NewFileURLPart("https://example.com/a.png", "image/png"),
		a2a.NewDataPart(map[string]any{"k": "v"}),
	}
	for _, p := range parts {
		w := partToWire(p)
		got := partFromWire(w)
		if !reflect.DeepEqual(canon(t, p), canon(t, got)) {
			t.Errorf("round trip changed part:\n got %#v\nwant %#v", got, p)
		}
	}
}

func TestDataPartCompatWrap(t *testing.T) {
	// Non-object data values wrap for the wire and unwrap on the way in.
	p := a2a.NewDataPart("scalar")
	w := partToWire(p)
	var obj map[string]any
	if err := json.Unmarshal(w.Data, &obj); err != nil {
		t.Fatalf("wrapped data not an object: %v", err)
	}
	if obj["value"] != "scalar" {
		t.Errorf("wrapped value = %#v", obj)
	}
	back := partFromWire(w)
	if back.Data() != "scalar" {
		t.Errorf("unwrap = %#v", back.Data())
	}
}

func TestPartsFromWireCaps(t *testing.T) {
	hostile := make([]wirePart, maxPartsPerMessage+50)
	for i := range hostile {
		hostile[i] = wirePart{Kind: "text", Text: "x"}
	}
	if got := partsFromWire(hostile); len(got) != maxPartsPerMessage {
		t.Fatalf("capped parts = %d, want %d", len(got), maxPartsPerMessage)
	}
}

func TestTaskGoldenFromFixture(t *testing.T) {
	raw := resultOf(t, loadFixture(t, "send-task-result.json"))
	ev, final, err := eventFromResult(raw)
	if err != nil {
		t.Fatalf("eventFromResult: %v", err)
	}
	if final {
		t.Error("task result must not be final")
	}
	task, ok := ev.(*a2a.Task)
	if !ok {
		t.Fatalf("event type %T, want *a2a.Task", ev)
	}

	if string(task.ID) != "225d6247-06ba-4cda-a08b-33ae35c8dcfa" {
		t.Errorf("task id = %q", task.ID)
	}
	if task.ContextID != "05217e44-7e9f-473e-ab4f-2c2dde50a2b1" {
		t.Errorf("context id = %q", task.ContextID)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		t.Errorf("state = %v", task.Status.State)
	}
	if task.Status.Message == nil || task.Status.Message.Role != a2a.MessageRoleAgent {
		t.Fatalf("status message = %#v", task.Status.Message)
	}
	if text := task.Status.Message.Parts[0].Text(); text != "pong" {
		t.Errorf("status message text = %q", text)
	}
	if len(task.Artifacts) != 1 || string(task.Artifacts[0].ID) != "9b6934dd-37e3-4eb1-8766-962efaab63a1" {
		t.Errorf("artifacts = %#v", task.Artifacts)
	}
	if len(task.History) != 1 || task.History[0].Role != a2a.MessageRoleUser {
		t.Errorf("history = %#v", task.History)
	}

	// Golden both directions: the wire re-encoding must equal the
	// fixture's result object (kind included, since the fixture carries
	// it and the round trip restores it).
	rewired := taskToWire(task)
	rewired.Kind = "task"
	if !reflect.DeepEqual(canon(t, &rewired), canon(t, json.RawMessage(raw))) {
		got, _ := json.Marshal(&rewired)
		t.Errorf("task round trip mismatch:\n got %s\nwant %s", got, raw)
	}
}

func TestMessageGoldenFromFixture(t *testing.T) {
	raw := resultOf(t, loadFixture(t, "send-message-result.json"))
	ev, _, err := eventFromResult(raw)
	if err != nil {
		t.Fatalf("eventFromResult: %v", err)
	}
	msg, ok := ev.(*a2a.Message)
	if !ok {
		t.Fatalf("event type %T, want *a2a.Message", ev)
	}
	if msg.ID != "1a2b3c4d-5e6f-4a5b-8c9d-0e1f2a3b4c5d" || msg.Role != a2a.MessageRoleAgent {
		t.Errorf("message = %#v", msg)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Text() != "pong" {
		t.Errorf("parts = %#v", msg.Parts)
	}
}

func TestStatusUpdateTranslation(t *testing.T) {
	raw := `{"taskId":"t1","contextId":"c1","kind":"status-update","status":{"state":"input-required","message":{"role":"agent","parts":[{"kind":"text","text":"seat?"}],"messageId":"m1"},"timestamp":"2025-04-02T16:59:35Z"},"final":true}`
	ev, final, err := eventFromResult(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("eventFromResult: %v", err)
	}
	su, ok := ev.(*a2a.TaskStatusUpdateEvent)
	if !ok {
		t.Fatalf("event type %T", ev)
	}
	if !final {
		t.Error("final flag lost")
	}
	if su.TaskID != "t1" || su.ContextID != "c1" || su.Status.State != a2a.TaskStateInputRequired {
		t.Errorf("status update = %#v", su)
	}
	if su.Status.Message == nil || su.Status.Message.Parts[0].Text() != "seat?" {
		t.Errorf("status message = %#v", su.Status.Message)
	}

	// Reverse direction sets final per the 0.3 convention.
	su.Status.State = a2a.TaskStateCompleted
	w := statusUpdateToWire(su)
	if !w.Final || w.Kind != "status-update" || w.Status.State != "completed" {
		t.Errorf("statusUpdateToWire = %+v", w)
	}
	su.Status.State = a2a.TaskStateInputRequired
	if w := statusUpdateToWire(su); !w.Final {
		t.Error("input-required must map to final:true")
	}
	su.Status.State = a2a.TaskStateWorking
	if w := statusUpdateToWire(su); w.Final {
		t.Error("working must not be final")
	}
}

func TestArtifactUpdateTranslation(t *testing.T) {
	raw := `{"taskId":"t1","contextId":"c1","kind":"artifact-update","artifact":{"artifactId":"a1","name":"doc","parts":[{"kind":"text","text":"chunk"}]},"append":true,"lastChunk":true}`
	ev, _, err := eventFromResult(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("eventFromResult: %v", err)
	}
	au, ok := ev.(*a2a.TaskArtifactUpdateEvent)
	if !ok {
		t.Fatalf("event type %T", ev)
	}
	if !au.Append || !au.LastChunk || au.Artifact == nil || string(au.Artifact.ID) != "a1" || au.Artifact.Name != "doc" {
		t.Errorf("artifact update = %#v", au)
	}

	back := artifactUpdateToWire(au)
	if back.Kind != "artifact-update" || !back.Append || !back.LastChunk || back.Artifact.ArtifactID != "a1" {
		t.Errorf("artifactUpdateToWire = %+v", back)
	}
}

func TestKindSniffing(t *testing.T) {
	// Some early 0.3 servers omit "kind" on results; shape sniffing must
	// recover the type.
	cases := []struct {
		raw  string
		want string
	}{
		{`{"id":"t","contextId":"c","status":{"state":"working"}}`, "task"},
		{`{"taskId":"t","contextId":"c","status":{"state":"completed"},"final":true}`, "status-update"},
		{`{"taskId":"t","artifact":{"artifactId":"a","parts":[]}}`, "artifact-update"},
		{`{"messageId":"m","role":"agent","parts":[]}`, "message"},
		{`{"foo":1}`, "unknown"},
		{`{"kind":"???","foo":1}`, "unknown"},
	}
	for _, c := range cases {
		if got := resultKind(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("resultKind(%s) = %q, want %q", c.raw, got, c.want)
		}
		// Only unknown kinds error from eventFromResult.
		_, _, err := eventFromResult(json.RawMessage(c.raw))
		if c.want == "unknown" && err == nil {
			t.Errorf("expected error for %s", c.raw)
		}
		if c.want != "unknown" && err != nil {
			t.Errorf("unexpected error for %s: %v", c.raw, err)
		}
	}
}

func TestPushConfigRoundTrip(t *testing.T) {
	cfg := &a2a.PushConfig{
		TaskID: "task-1",
		ID:     "cfg-1",
		Token:  "tok",
		URL:    "https://client.example/hook",
		Auth:   &a2a.PushAuthInfo{Scheme: "Bearer", Credentials: "secret"},
	}
	w := pushToWire(cfg)
	if w.TaskID != "task-1" || w.Config.ID != "cfg-1" || w.Config.URL != "https://client.example/hook" {
		t.Fatalf("pushToWire = %+v", w)
	}
	if w.Config.Auth == nil || len(w.Config.Auth.Schemes) != 1 || w.Config.Auth.Schemes[0] != "Bearer" {
		t.Fatalf("auth = %+v", w.Config.Auth)
	}
	back := pushFromWire(w)
	if back.TaskID != cfg.TaskID || back.ID != cfg.ID || back.Token != cfg.Token || back.URL != cfg.URL {
		t.Errorf("round trip = %+v", back)
	}
	if back.Auth == nil || back.Auth.Scheme != "Bearer" || back.Auth.Credentials != "secret" {
		t.Errorf("auth round trip = %+v", back.Auth)
	}
}

func TestSendParamsFromRequest(t *testing.T) {
	fixed := "gen-1"
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hi"))
	req := &a2a.SendMessageRequest{Message: msg}
	p, err := sendParamsFromRequest(req, func() string { return fixed })
	if err != nil {
		t.Fatalf("sendParams: %v", err)
	}
	if p.Message.MessageID != msg.ID {
		t.Errorf("message id = %q, want existing %q", p.Message.MessageID, msg.ID)
	}
	if p.Message.Role != "user" || len(p.Message.Parts) != 1 || p.Message.Parts[0].Kind != "text" {
		t.Errorf("message = %+v", p.Message)
	}
	// No config in the request means no configuration on the wire (SDK
	// parity: nothing beyond the user's message is sent).
	if p.Config != nil {
		t.Fatalf("config emitted although the request had none: %+v", p.Config)
	}

	// Empty id gets generated; config maps fields and inverts ReturnImmediately.
	msg2 := &a2a.Message{Role: a2a.MessageRoleAgent, Parts: a2a.ContentParts{a2a.NewTextPart("yo")}}
	hist := 5
	req2 := &a2a.SendMessageRequest{
		Message: msg2,
		Config: &a2a.SendMessageConfig{
			AcceptedOutputModes: []string{"text/plain"},
			ReturnImmediately:   true,
			HistoryLength:       &hist,
			PushConfig:          &a2a.PushConfig{URL: "https://hook", Token: "t"},
		},
		Metadata: map[string]any{"k": "v"},
	}
	p2, err := sendParamsFromRequest(req2, func() string { return fixed })
	if err != nil {
		t.Fatalf("sendParams2: %v", err)
	}
	if p2.Message.MessageID != fixed {
		t.Errorf("generated id = %q", p2.Message.MessageID)
	}
	if p2.Message.Role != "agent" {
		t.Errorf("role = %q", p2.Message.Role)
	}
	if p2.Config.Blocking == nil || *p2.Config.Blocking {
		t.Errorf("blocking = %v, want false for ReturnImmediately", p2.Config.Blocking)
	}
	if len(p2.Config.AcceptedOutputModes) != 1 || p2.Config.AcceptedOutputModes[0] != "text/plain" {
		t.Errorf("modes = %v", p2.Config.AcceptedOutputModes)
	}
	if p2.Config.HistoryLength == nil || *p2.Config.HistoryLength != 5 {
		t.Errorf("historyLength = %v", p2.Config.HistoryLength)
	}
	if p2.Config.PushConfig == nil || p2.Config.PushConfig.URL != "https://hook" {
		t.Errorf("push config = %+v", p2.Config.PushConfig)
	}
	if p2.Metadata["k"] != "v" {
		t.Errorf("metadata = %v", p2.Metadata)
	}

	// Nil message is rejected.
	if _, err := sendParamsFromRequest(&a2a.SendMessageRequest{}, func() string { return fixed }); err == nil {
		t.Error("nil message must error")
	}
}
