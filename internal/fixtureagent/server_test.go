package fixtureagent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// A2A v1.0 task states as they appear on the wire.
const (
	stateSubmitted     = "TASK_STATE_SUBMITTED"
	stateWorking       = "TASK_STATE_WORKING"
	stateInputRequired = "TASK_STATE_INPUT_REQUIRED"
	stateCompleted     = "TASK_STATE_COMPLETED"
	stateFailed        = "TASK_STATE_FAILED"
	stateCanceled      = "TASK_STATE_CANCELED"
)

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestAgentCardServed(t *testing.T) {
	c, ts := startFixture(t)
	ctx := testCtx(t)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/.well-known/agent-card.json", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatalf("fetch card: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var card struct {
		Name         string `json:"name"`
		Version      string `json:"version"`
		Capabilities struct {
			Streaming         bool `json:"streaming"`
			PushNotifications bool `json:"pushNotifications"`
		} `json:"capabilities"`
		SupportedInterfaces []struct {
			URL             string `json:"url"`
			ProtocolBinding string `json:"protocolBinding"`
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"supportedInterfaces"`
		Skills []struct {
			ID string `json:"id"`
		} `json:"skills"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatalf("decode card: %v", err)
	}
	if card.Name != AgentName {
		t.Errorf("name = %q, want %q", card.Name, AgentName)
	}
	if card.Version != "1.0" {
		t.Errorf("version = %q, want 1.0", card.Version)
	}
	if !card.Capabilities.Streaming || !card.Capabilities.PushNotifications {
		t.Errorf("capabilities = %+v, want streaming and pushNotifications", card.Capabilities)
	}
	if len(card.SupportedInterfaces) != 1 {
		t.Fatalf("supportedInterfaces = %+v, want exactly one", card.SupportedInterfaces)
	}
	iface := card.SupportedInterfaces[0]
	if iface.URL != ts.URL {
		t.Errorf("interface url = %q, want %q (the final URL)", iface.URL, ts.URL)
	}
	if iface.ProtocolBinding != "JSONRPC" || iface.ProtocolVersion != "1.0" {
		t.Errorf("interface = %+v, want JSONRPC/1.0", iface)
	}
	if len(card.Skills) < 2 {
		t.Errorf("skills = %d, want at least 2", len(card.Skills))
	}
}

// TestKeywordBehaviors drives the simple completing/failing keywords
// end-to-end through unary SendMessage calls.
func TestKeywordBehaviors(t *testing.T) {
	tests := []struct {
		name        string
		message     string
		wantState   string
		wantTextSub string
	}{
		{name: "echo is the default", message: "hello fixture", wantState: stateCompleted, wantTextSub: "echo: hello fixture"},
		{name: "echo exact", message: "echo", wantState: stateCompleted, wantTextSub: "echo:"},
		{name: "keywords are case-insensitive", message: "FAIL", wantState: stateFailed, wantTextSub: "deliberate failure"},
		{name: "fail", message: "fail now", wantState: stateFailed, wantTextSub: "deliberate failure"},
		{name: "slow completes", message: "slow 0.3", wantState: stateCompleted, wantTextSub: "slept"},
		{name: "skills lists keywords", message: "skills", wantState: stateCompleted, wantTextSub: "stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := startFixture(t)
			ctx := testCtx(t)
			task := c.expectTaskResult(c.sendMessage(ctx, tt.message, sendOpts{}))
			if task.Status.State != tt.wantState {
				t.Fatalf("state = %s, want %s", task.Status.State, tt.wantState)
			}
			got := task.Status.Message.text()
			if !strings.Contains(got, tt.wantTextSub) {
				t.Errorf("status message = %q, want substring %q", got, tt.wantTextSub)
			}
		})
	}
}

func TestA2UIUnknownScriptRepliesWithHelp(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)
	msg := c.expectMessageResult(c.sendMessage(ctx, "a2ui bogus", sendOpts{}))
	if text := msg.text(); !strings.Contains(text, "unknown script") {
		t.Errorf("help reply = %q, want it to list the a2ui scripts", text)
	}
}

func TestStreamChunks(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	events := c.streamEvents(ctx, "stream 5", sendOpts{})
	if len(events) < 7 {
		t.Fatalf("got %d events, want at least 7 (task snapshot, working, 5 chunks, completed)", len(events))
	}
	if events[0].kind() != "task" {
		t.Errorf("first event kind = %s, want task snapshot", events[0].kind())
	}
	if events[0].Task.Status.State != stateSubmitted {
		t.Errorf("snapshot state = %s, want %s", events[0].Task.Status.State, stateSubmitted)
	}

	var chunks []string
	var lastState, lastText string
	for _, ev := range events {
		if ev.StatusUpdate == nil {
			continue
		}
		lastState = ev.StatusUpdate.Status.State
		lastText = ev.StatusUpdate.Status.Message.text()
		if text := lastText; strings.HasPrefix(text, "chunk ") {
			chunks = append(chunks, text)
		}
	}
	want := []string{"chunk 1/5", "chunk 2/5", "chunk 3/5", "chunk 4/5", "chunk 5/5"}
	if len(chunks) != len(want) {
		t.Fatalf("chunks = %v, want %v", chunks, want)
	}
	for i := range want {
		if chunks[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, chunks[i], want[i])
		}
	}
	if lastState != stateCompleted {
		t.Errorf("last status state = %s, want %s", lastState, stateCompleted)
	}
	if lastText != "stream complete: 5 chunks" {
		t.Errorf("last status message = %q", lastText)
	}
}

func TestInputRequiredRoundTrip(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	task := c.expectTaskResult(c.sendMessage(ctx, "inputreq", sendOpts{}))
	if task.Status.State != stateInputRequired {
		t.Fatalf("state = %s, want %s", task.Status.State, stateInputRequired)
	}
	if !strings.Contains(task.Status.Message.text(), "favorite color") {
		t.Errorf("question = %q, want it to mention the question", task.Status.Message.text())
	}

	cont := c.expectTaskResult(c.sendMessage(ctx, "blue", sendOpts{
		taskID:    task.ID,
		contextID: task.ContextID,
	}))
	if cont.Status.State != stateCompleted {
		t.Fatalf("continuation state = %s, want %s", cont.Status.State, stateCompleted)
	}
	if got := cont.Status.Message.text(); got != "you answered: blue" {
		t.Errorf("continuation answer = %q, want %q", got, "you answered: blue")
	}
}

func TestArtifact(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	task := c.expectTaskResult(c.sendMessage(ctx, "artifact", sendOpts{}))
	if task.Status.State != stateCompleted {
		t.Fatalf("state = %s, want %s", task.Status.State, stateCompleted)
	}
	if len(task.Artifacts) != 1 {
		t.Fatalf("artifacts = %d, want 1", len(task.Artifacts))
	}
	art := task.Artifacts[0]
	if art.ID == "" {
		t.Error("artifact id is empty")
	}
	if !strings.HasPrefix(art.text(), "# Fixture artifact") {
		t.Errorf("artifact text = %q, want markdown heading", art.text())
	}
	if art.Parts[0].MediaType != "text/markdown" {
		t.Errorf("artifact mediaType = %q, want text/markdown", art.Parts[0].MediaType)
	}
}

func TestArtifactAppend(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	events := c.streamEvents(ctx, "artifact append", sendOpts{})
	var updates []*wireArtifactUpd
	lastState := ""
	for _, ev := range events {
		if ev.ArtifactUpdate != nil {
			updates = append(updates, ev.ArtifactUpdate)
		}
		if ev.StatusUpdate != nil {
			lastState = ev.StatusUpdate.Status.State
		}
	}
	if len(updates) != 3 {
		t.Fatalf("artifact updates = %d, want 3 (create + 2 appends)", len(updates))
	}
	if updates[0].Append {
		t.Error("first artifact update must not append")
	}
	if !updates[1].Append || updates[1].LastChunk {
		t.Errorf("second update append=%v lastChunk=%v, want true/false", updates[1].Append, updates[1].LastChunk)
	}
	if !updates[2].Append || !updates[2].LastChunk {
		t.Errorf("final update append=%v lastChunk=%v, want true/true", updates[2].Append, updates[2].LastChunk)
	}
	if updates[0].Artifact.ID != updates[2].Artifact.ID {
		t.Errorf("artifact ids differ: %q vs %q", updates[0].Artifact.ID, updates[2].Artifact.ID)
	}
	want := "# Appended fixture artifact\n\nSecond stanza, appended.\n\nThird and final stanza (lastChunk)."
	var accumulated string
	for _, upd := range updates {
		accumulated += upd.Artifact.text()
	}
	if accumulated != want {
		t.Errorf("accumulated artifact text = %q, want %q", accumulated, want)
	}
	if lastState != stateCompleted {
		t.Errorf("final state = %s, want %s", lastState, stateCompleted)
	}
}

func TestCancelWhileWorking(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	// returnImmediately yields the task snapshot right away while the
	// detached execution keeps working, so the test can drive CancelTask.
	task := c.expectTaskResult(c.sendMessage(ctx, "cancelme 30", sendOpts{
		config: map[string]any{"returnImmediately": true},
	}))
	working := c.waitTaskState(ctx, task.ID, stateWorking, 5*time.Second)

	cancelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw := c.call(cancelCtx, "CancelTask", map[string]any{"id": working.ID})
	var canceledTask wireTask
	if err := json.Unmarshal(raw, &canceledTask); err != nil || canceledTask.Status.State == "" {
		t.Fatalf("CancelTask result is not a task: %v (raw %s)", err, raw)
	}
	if canceledTask.Status.State != stateCanceled {
		t.Fatalf("CancelTask state = %s, want %s", canceledTask.Status.State, stateCanceled)
	}
	after := c.getTask(ctx, working.ID)
	if after.Status.State != stateCanceled {
		t.Errorf("stored task state after cancel = %s, want %s", after.Status.State, stateCanceled)
	}
}

// a2uiDecode extracts and decodes the single A2UI data part of an agent
// message.
func a2uiDecode(t *testing.T, msg *wireMessage) []a2ui.Envelope {
	t.Helper()
	for _, p := range msg.Parts {
		if p.Data == nil {
			continue
		}
		mt, _ := p.Metadata["mimeType"].(string)
		if mt != a2ui.MimeTypeA2UI {
			continue
		}
		envs, err := a2ui.DecodeEnvelopes(p.Data)
		if err != nil {
			t.Fatalf("DecodeEnvelopes: %v", err)
		}
		return envs
	}
	t.Fatalf("message has no application/a2ui+json data part: %+v", msg.Parts)
	return nil
}

// a2uiPart builds the wire form of a data part carrying raw JSON with the
// A2UI mimeType marker.
func a2uiPart(raw json.RawMessage) map[string]any {
	return map[string]any{
		"data":     raw,
		"metadata": map[string]any{"mimeType": a2ui.MimeTypeA2UI},
	}
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

func applyEnvelopes(t *testing.T, eng *a2ui.Engine, envs []a2ui.Envelope) {
	t.Helper()
	if errs := eng.Apply(testCtx(t), envs); len(errs) != 0 {
		t.Fatalf("engine apply errors: %v", errs)
	}
}

func TestA2UIFormSurface(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	msg := c.expectMessageResult(c.sendMessage(ctx, "a2ui form", sendOpts{}))
	envs := a2uiDecode(t, msg)
	if len(envs) != 1 || envs[0].CreateSurface == nil {
		t.Fatalf("envelopes = %+v, want a single createSurface", envs)
	}
	sid := envs[0].CreateSurface.SurfaceID
	if sid == "" {
		t.Fatal("surfaceId is empty")
	}

	// Round-trip through the engine: the fixture's surfaces must apply
	// cleanly.
	eng := a2ui.NewEngine()
	applyEnvelopes(t, eng, envs)
	surface := eng.Surface(sid)
	if surface == nil {
		t.Fatal("surface missing after apply")
	}

	props := func(id string) any {
		comp, ok := surface.Components[id]
		if !ok {
			t.Fatalf("component %q missing", id)
		}
		return comp.Props
	}
	if tf, ok := props("name-field").(*a2ui.TextFieldProps); !ok || tf.Variant != "shortText" {
		t.Errorf("name-field props = %#v, want shortText TextField", props("name-field"))
	}
	if tf, ok := props("bio-field").(*a2ui.TextFieldProps); !ok || tf.Variant != "longText" {
		t.Errorf("bio-field props = %#v, want longText TextField", props("bio-field"))
	}
	if tf, ok := props("age-field").(*a2ui.TextFieldProps); !ok || tf.Variant != "number" {
		t.Errorf("age-field props = %#v, want number TextField", props("age-field"))
	}
	if _, ok := props("subscribe-check").(*a2ui.CheckBoxProps); !ok {
		t.Error("subscribe-check is not a CheckBox")
	}
	if cp, ok := props("plan-picker").(*a2ui.ChoicePickerProps); !ok || cp.Variant != "singleSelection" || len(cp.Options) != 3 {
		t.Errorf("plan-picker = %#v, want singleSelection with 3 options", props("plan-picker"))
	}
	if cp, ok := props("interests-picker").(*a2ui.ChoicePickerProps); !ok || cp.Variant != "multipleSelection" {
		t.Errorf("interests-picker = %#v, want multipleSelection", props("interests-picker"))
	}
	if sl, ok := props("volume-slider").(*a2ui.SliderProps); !ok || sl.Min == nil || sl.Max == nil {
		t.Errorf("volume-slider = %#v, want Slider with min/max", props("volume-slider"))
	}
	if dt, ok := props("meeting-at").(*a2ui.DateTimeInputProps); !ok || !dt.EnableDate || !dt.EnableTime {
		t.Errorf("meeting-at = %#v, want DateTimeInput with date+time", props("meeting-at"))
	}
	if btn, ok := props("submit-btn").(*a2ui.ButtonProps); !ok || btn.Action == nil || btn.Action.Event == nil || btn.Action.Event.Name != "submit" {
		t.Errorf("submit-btn = %#v, want Button with event action submit", props("submit-btn"))
	}
	if txt, ok := props("name-preview").(*a2ui.TextProps); !ok || txt.Text.Kind != a2ui.KindCall {
		t.Errorf("name-preview = %#v, want Text bound via a function call", props("name-preview"))
	}
	if plan, ok := (a2ui.EvalContext{Root: surface.DataModel}).Resolve("/plan"); !ok || plan != "pro" {
		t.Errorf("data model /plan = %v, want pro", surface.DataModel)
	}

	// The button press: send a renderer action continuing the CONTEXT (the
	// a2ui flow is taskless; the reply carried only a contextId).
	action := a2ui.NewActionMessage(a2ui.VersionV1, sid, "submit-btn", "submit", map[string]any{"name": "Ada Lovelace"})
	if msg.ContextID == "" {
		t.Fatal("reply message has no contextId to continue on")
	}
	reply := c.expectMessageResult(c.sendMessage(ctx, "", sendOpts{
		contextID: msg.ContextID,
		parts:     []any{a2uiPart(mustMarshal(t, action))},
	}))
	upd := a2uiDecode(t, reply)
	if len(upd) != 2 {
		t.Fatalf("action reply envelopes = %d, want 2 (updateDataModel + updateComponents)", len(upd))
	}
	applyEnvelopes(t, eng, upd)

	surface = eng.Surface(sid)
	if name, ok := (a2ui.EvalContext{Root: surface.DataModel}).Resolve("/submitted/name"); !ok || name != "Ada Lovelace" {
		t.Errorf("/submitted/name = %v ok=%v, want Ada Lovelace", name, ok)
	}
	btn, ok := surface.Components["submit-btn"]
	if !ok || btn.Component != "Text" {
		t.Fatalf("submit-btn after update = %#v, want Text", btn)
	}
	if txt, ok := btn.Props.(*a2ui.TextProps); !ok || txt.Text.Kind != a2ui.KindLiteral || txt.Text.Literal != "Submitted ✓" {
		t.Errorf("submit-btn text = %#v, want literal 'Submitted ✓'", btn.Props)
	}
}

func TestA2UIDynamicSurface(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	msg := c.expectMessageResult(c.sendMessage(ctx, "a2ui dynamic", sendOpts{}))
	envs := a2uiDecode(t, msg)
	if len(envs) != 1 || envs[0].CreateSurface == nil {
		t.Fatalf("envelopes = %+v, want a single createSurface", envs)
	}
	sid := envs[0].CreateSurface.SurfaceID
	eng := a2ui.NewEngine()
	applyEnvelopes(t, eng, envs)
	surface := eng.Surface(sid)

	if txt, ok := surface.Components["counter-text"].Props.(*a2ui.TextProps); !ok || txt.Text.Kind != a2ui.KindCall || txt.Text.Call.Name != "formatString" {
		t.Errorf("counter-text = %#v, want formatString-bound Text", surface.Components["counter-text"].Props)
	}
	eval := a2ui.EvalContext{Root: surface.DataModel, Funcs: a2ui.NewFunctionRegistry()}
	if got := surface.Components["counter-text"].Props.(*a2ui.TextProps).Text.EvalString(eval); got != "count=1, label=initial" {
		t.Errorf("bound text evaluates to %q, want %q", got, "count=1, label=initial")
	}

	// Pressing refresh: attach the current data model (as a renderer that
	// opted into sendDataModel would) and expect a whole-model update.
	action := a2ui.NewActionMessage(a2ui.VersionV1, sid, "refresh-btn", "refresh", nil)
	reply := c.expectMessageResult(c.sendMessage(ctx, "", sendOpts{
		contextID: msg.ContextID,
		parts:     []any{a2uiPart(mustMarshal(t, action))},
		metadata: map[string]any{
			"a2uiRendererDataModel": map[string]any{
				"version":  a2ui.VersionV1,
				"surfaces": map[string]any{sid: map[string]any{"count": 41, "label": "before"}},
			},
		},
	}))
	upd := a2uiDecode(t, reply)
	if len(upd) != 1 || upd[0].UpdateDataModel == nil {
		t.Fatalf("action reply = %+v, want one updateDataModel", upd)
	}
	applyEnvelopes(t, eng, upd)

	surface = eng.Surface(sid)
	eval = a2ui.EvalContext{Root: surface.DataModel, Funcs: a2ui.NewFunctionRegistry()}
	if count, ok := eval.Resolve("/count"); !ok || count != float64(42) {
		t.Errorf("/count = %v ok=%v, want 42 (attached 41 + 1)", count, ok)
	}
	if label, ok := eval.Resolve("/label"); !ok || label != "refreshed" {
		t.Errorf("/label = %v ok=%v, want refreshed", label, ok)
	}
}

func TestA2UIListSurface(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	msg := c.expectMessageResult(c.sendMessage(ctx, "a2ui list", sendOpts{}))
	envs := a2uiDecode(t, msg)
	if len(envs) != 1 || envs[0].CreateSurface == nil {
		t.Fatalf("envelopes = %+v, want a single createSurface", envs)
	}
	sid := envs[0].CreateSurface.SurfaceID
	eng := a2ui.NewEngine()
	applyEnvelopes(t, eng, envs)
	surface := eng.Surface(sid)

	list, ok := surface.Components["demo-list"].Props.(*a2ui.ListProps)
	if !ok {
		t.Fatalf("demo-list props = %#v, want List", surface.Components["demo-list"].Props)
	}
	if !list.Children.IsTemplate() || list.Children.TemplateID != "list-item" || list.Children.TemplatePath != "/items" {
		t.Fatalf("list children = %+v, want template {list-item, /items}", list.Children)
	}
	items, ok := (a2ui.EvalContext{Root: surface.DataModel}).Resolve("/items")
	if !ok {
		t.Fatal("data model has no /items")
	}
	elems, ok := items.([]any)
	if !ok || len(elems) != 5 {
		t.Fatalf("/items = %#v, want a 5-element array", items)
	}

	// Evaluate the template text per element, exactly like the renderer
	// does when instantiating the list.
	tmpl, ok := surface.Components["list-item"].Props.(*a2ui.TextProps)
	if !ok {
		t.Fatalf("list-item props = %#v, want Text", surface.Components["list-item"].Props)
	}
	funcs := a2ui.NewFunctionRegistry()
	want := []string{"0. echo", "1. stream", "2. artifact", "3. push", "4. a2ui"}
	for i, elem := range elems {
		idx := i
		got := tmpl.Text.EvalString(a2ui.EvalContext{Root: surface.DataModel, Scope: elem, Index: &idx, Funcs: funcs})
		if got != want[i] {
			t.Errorf("list item %d renders %q, want %q", i, got, want[i])
		}
	}
}

func TestPushWebhookReceivesEvents(t *testing.T) {
	c, _ := startFixture(t)
	ctx := testCtx(t)

	// A local webhook capturing bodies and headers.
	var mu sync.Mutex
	var bodies []string
	var headers []http.Header
	arrived := make(chan struct{}, 32)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("webhook read: %v", err)
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		select {
		case arrived <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(hook.Close)

	// Stream the "push" script; the first SSE frame carries the task id,
	// which is then used to register the webhook mid-flight.
	resp, br := c.openStream(ctx, "push", sendOpts{})
	defer func() { _ = resp.Body.Close() }()
	frame, ok := readSSEFrame(br)
	if !ok {
		t.Fatal("stream closed before the first frame")
	}
	var first rpcResp
	if err := json.Unmarshal(frame, &first); err != nil {
		t.Fatalf("decode first frame: %v", err)
	}
	task := decodeEvent(first.Result)
	if task.Task == nil {
		t.Fatalf("first frame kind = %s, want task snapshot", task.kind())
	}
	c.call(ctx, "CreateTaskPushNotificationConfig", map[string]any{
		"taskId": task.Task.ID,
		"url":    hook.URL,
		"token":  "fixture-token",
	})

	// Drain the stream (the push script runs ~2.4s) then wait for the
	// webhook to receive at least one POST.
	for _, f := range readAllSSEFrames(br) {
		var out rpcResp
		if err := json.Unmarshal(f, &out); err != nil {
			t.Fatalf("decode frame %s: %v", f, err)
		}
		if out.Error != nil {
			t.Fatalf("sse frame error %d: %s", out.Error.Code, out.Error.Message)
		}
	}

	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("webhook received nothing within 5s (bodies so far: %d)", len(bodies))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("no webhook bodies recorded")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(bodies[0]), &payload); err != nil {
		t.Fatalf("webhook body is not decodable JSON: %v (body %s)", err, bodies[0])
	}
	if _, ok := payload["task"]; !ok {
		if _, ok := payload["statusUpdate"]; !ok {
			t.Errorf("webhook body carries neither task nor statusUpdate: %s", bodies[0])
		}
	}
	if got := headers[0].Get("A2A-Notification-Token"); got != "fixture-token" {
		t.Errorf("notification token header = %q, want fixture-token", got)
	}
}

func TestStartEphemeralAndShutdown(t *testing.T) {
	base, shutdown, err := Start("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Errorf("base URL = %q, want an explicit loopback URL", base)
	}

	ctx := testCtx(t)
	c := newRawClient(t, base)
	task := c.expectTaskResult(c.sendMessage(ctx, "ephemeral", sendOpts{}))
	if task.Status.State != stateCompleted || !strings.Contains(task.Status.Message.text(), "echo: ephemeral") {
		t.Errorf("ephemeral echo = %s / %q", task.Status.State, task.Status.Message.text())
	}

	shutdown()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/agent-card.json", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	deadlineClient := &http.Client{Timeout: 2 * time.Second}
	if _, err := deadlineClient.Do(req); err == nil {
		t.Error("server still serving after shutdown")
	}
}
