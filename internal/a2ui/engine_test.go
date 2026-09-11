package a2ui

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func apply(t *testing.T, e *Engine, raw string) []ApplyError {
	t.Helper()
	envs, err := DecodeEnvelopes([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return e.Apply(context.Background(), envs)
}

func TestApplyLifecycle(t *testing.T) {
	e := NewEngine()
	errs := apply(t, e, `[
		{"version":"v1.0","createSurface":{"surfaceId":"s","catalogId":"c","sendDataModel":true}},
		{"version":"v1.0","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Text","text":"hi"}]}},
		{"version":"v1.0","updateDataModel":{"surfaceId":"s","path":"/name","value":"Alice"}}
	]`)
	if len(errs) != 0 {
		t.Fatalf("apply errors: %v", errs)
	}
	s := e.Surface("s")
	if s == nil {
		t.Fatal("surface missing")
	}
	if _, ok := s.Components["root"]; !ok {
		t.Error("root component missing")
	}
	if v, _ := (EvalContext{Root: s.DataModel}).Resolve("/name"); v != "Alice" {
		t.Errorf("data model = %#v", s.DataModel)
	}
	if !s.SendDataModel {
		t.Error("sendDataModel lost")
	}
	// Upsert semantics: second update at a nested path creates keys.
	errs = apply(t, e, `{"version":"v1.0","updateDataModel":{"surfaceId":"s","path":"/a/b/c","value":1}}`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if v, ok := (EvalContext{Root: e.Surface("s").DataModel}).Resolve("/a/b/c"); !ok || v != float64(1) {
		t.Errorf("upsert /a/b/c = %v ok=%v", v, ok)
	}
	// Delete via null.
	errs = apply(t, e, `{"version":"v1.0","updateDataModel":{"surfaceId":"s","path":"/name","value":null}}`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, ok := (EvalContext{Root: e.Surface("s").DataModel}).Resolve("/name"); ok {
		t.Error("null value should delete the key")
	}
	// Whole-model replace (no path).
	errs = apply(t, e, `{"version":"v1.0","updateDataModel":{"surfaceId":"s","value":{"x":1}}}`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if _, ok := (EvalContext{Root: e.Surface("s").DataModel}).Resolve("/x"); !ok {
		t.Error("whole-model replace failed")
	}
	// Delete the surface.
	errs = apply(t, e, `{"version":"v1.0","deleteSurface":{"surfaceId":"s"}}`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if e.Surface("s") != nil {
		t.Error("surface still present after delete")
	}
}

func TestApplyStructuralErrors(t *testing.T) {
	e := NewEngine()
	// createSurface on existing ID.
	errs := apply(t, e, `[
		{"version":"v1.0","createSurface":{"surfaceId":"s"}},
		{"version":"v1.0","createSurface":{"surfaceId":"s"}}
	]`)
	if len(errs) != 1 || !errors.Is(errs[0].Err, ErrSurfaceExists) {
		t.Fatalf("errs = %v", errs)
	}
	// Messages for missing surfaces.
	errs = apply(t, e, `[
		{"version":"v1.0","updateComponents":{"surfaceId":"nope","components":[{"id":"root","component":"Text","text":"x"}]}},
		{"version":"v1.0","updateDataModel":{"surfaceId":"nope","value":1}},
		{"version":"v1.0","deleteSurface":{"surfaceId":"nope"}}
	]`)
	if len(errs) != 3 {
		t.Fatalf("want 3 errors, got %v", errs)
	}
	for _, ae := range errs {
		if !errors.Is(ae.Err, ErrNoSurface) {
			t.Errorf("err %v is not ErrNoSurface", ae)
		}
	}
}

// TestApplyNonTransactional pins sequential processing with per-envelope
// error capture (A2A extension "Processing Rules").
func TestApplyNonTransactional(t *testing.T) {
	e := NewEngine()
	errs := apply(t, e, `[
		{"version":"v1.0","updateComponents":{"surfaceId":"missing","components":[{"id":"root","component":"Text","text":"x"}]}},
		{"version":"v1.0","createSurface":{"surfaceId":"ok"}}
	]`)
	if len(errs) != 1 || errs[0].Index != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if e.Surface("ok") == nil {
		t.Error("second envelope must still apply after the first fails")
	}
}

// TestApplyUnknownVersionBestEffort: unknown versions record an error and
// still apply.
func TestApplyUnknownVersionBestEffort(t *testing.T) {
	e := NewEngine()
	errs := apply(t, e, `{"version":"v2.0","createSurface":{"surfaceId":"s"}}`)
	if len(errs) != 1 || !errors.Is(errs[0].Err, ErrUnknownVersion) {
		t.Fatalf("errs = %v", errs)
	}
	if e.Surface("s") == nil {
		t.Error("unknown-version envelope should still apply best-effort")
	}
}

func TestApplyChangeSetCallback(t *testing.T) {
	var got ChangeSet
	e := NewEngine(WithUpdateCallback(func(cs ChangeSet) { got = cs }))
	errs := apply(t, e, `[
		{"version":"v1.0","createSurface":{"surfaceId":"a"}},
		{"version":"v1.0","createSurface":{"surfaceId":"b"}},
		{"version":"v1.0","deleteSurface":{"surfaceId":"a"}}
	]`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	if len(got.Created) != 2 || len(got.Deleted) != 1 || got.Deleted[0] != "a" {
		t.Errorf("change set = %+v", got)
	}
}

func TestApplyInlineCreateSurface(t *testing.T) {
	// v1.0 single-message instantiation.
	e := NewEngine()
	errs := apply(t, e, `{"version":"v1.0","createSurface":{"surfaceId":"s","components":[
		{"id":"root","component":"Text","text":{"path":"/name"}}],"dataModel":{"name":"In"}}}`)
	if len(errs) != 0 {
		t.Fatal(errs)
	}
	s := e.Surface("s")
	root, err := s.Materialize()
	if err != nil {
		t.Fatal(err)
	}
	ctx := s.EvalContextFor(e.Functions())
	tp := root.Component.Props.(*TextProps)
	if got := tp.Text.EvalString(root.EvalContext(ctx)); got != "In" {
		t.Errorf("inline createSurface text = %q", got)
	}
}

func TestOutboundCapabilities(t *testing.T) {
	e := NewEngine()
	caps := e.OutboundCapabilities()
	renderer, ok := caps["a2uiRendererCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("renderer capabilities missing: %#v", caps)
	}
	v1, ok := renderer[VersionV1].(map[string]any)
	if !ok {
		t.Fatalf("v1.0 block missing: %#v", renderer)
	}
	ids, ok := v1["supportedCatalogIds"].([]string)
	if !ok || len(ids) != 1 || ids[0] != BasicCatalogIDV1 {
		t.Errorf("v1.0 supportedCatalogIds = %#v", v1["supportedCatalogIds"])
	}
	client, ok := caps["a2uiClientCapabilities"].(map[string]any)
	if !ok {
		t.Fatalf("client capabilities missing: %#v", caps)
	}
	v09, ok := client[VersionV09].(map[string]any)
	if !ok {
		t.Fatalf("v0.9 block missing: %#v", client)
	}
	if ids, _ := v09["supportedCatalogIds"].([]string); len(ids) != 1 || ids[0] != BasicCatalogIDV091 {
		t.Errorf("v0.9 supportedCatalogIds = %#v", v09["supportedCatalogIds"])
	}
	// Exact wire shape check.
	b, _ := json.Marshal(caps)
	want := `{"a2uiClientCapabilities":{"v0.9":{"supportedCatalogIds":["` + BasicCatalogIDV091 + `"]}},"a2uiRendererCapabilities":{"v1.0":{"supportedCatalogIds":["` + BasicCatalogIDV1 + `"]}}}`
	if string(b) != want {
		t.Errorf("wire shape:\n got %s\nwant %s", b, want)
	}
}

func TestDataModelMetadata(t *testing.T) {
	e := NewEngine()
	if got := e.DataModelMetadata(); got != nil {
		t.Errorf("no surfaces: %v", got)
	}
	apply(t, e, `[
		{"version":"v1.0","createSurface":{"surfaceId":"sync","sendDataModel":true}},
		{"version":"v1.0","createSurface":{"surfaceId":"nosync"}},
		{"version":"v1.0","updateDataModel":{"surfaceId":"sync","value":{"email":"a@b.c"}}}
	]`)
	md := e.DataModelMetadata()
	if md == nil {
		t.Fatal("metadata missing for syncing surface")
	}
	if md["version"] != VersionV1 {
		t.Errorf("version = %v", md["version"])
	}
	surfaces, ok := md["surfaces"].(map[string]any)
	if !ok || len(surfaces) != 1 {
		t.Fatalf("surfaces = %#v", md["surfaces"])
	}
	if surfaces["sync"].(map[string]any)["email"] != "a@b.c" {
		t.Errorf("sync model = %#v", surfaces["sync"])
	}
	// Per-surface query skips non-syncing surfaces.
	if got := e.DataModelMetadata("nosync"); got != nil {
		t.Errorf("nosync metadata = %v", got)
	}
}

func TestExtractEnvelopes(t *testing.T) {
	var data any
	_ = json.Unmarshal([]byte(`[{"version":"v1.0","createSurface":{"surfaceId":"x"}}]`), &data)
	envs, isA2UI := ExtractEnvelopes(data, map[string]any{"mimeType": "application/a2ui+json"})
	if !isA2UI || len(envs) != 1 {
		t.Fatalf("envs=%d isA2UI=%v", len(envs), isA2UI)
	}
	// Wrong mimeType: not A2UI.
	if _, isA2UI := ExtractEnvelopes(data, map[string]any{"mimeType": "application/json"}); isA2UI {
		t.Error("wrong mimeType must not extract")
	}
	// No metadata: not A2UI.
	if _, isA2UI := ExtractEnvelopes(data, nil); isA2UI {
		t.Error("nil metadata must not extract")
	}
	// Single object data tolerance.
	var single any
	_ = json.Unmarshal([]byte(`{"version":"v1.0","createSurface":{"surfaceId":"y"}}`), &single)
	envs, isA2UI = ExtractEnvelopes(single, map[string]any{"mimeType": MimeTypeA2UI})
	if !isA2UI || len(envs) != 1 || envs[0].CreateSurface.SurfaceID != "y" {
		t.Errorf("single-object tolerance failed: %v", envs)
	}
}

// TestHostileInputs guards against panics and runaway work on hostile data.
func TestHostileInputs(t *testing.T) {
	e := NewEngine()
	// Huge envelope rejected at decode.
	big := make([]byte, MaxEnvelopeBytes+2)
	for i := range big {
		big[i] = ' '
	}
	if _, err := DecodeEnvelopes(big); err == nil {
		t.Error("oversized envelope list must error")
	}
	// Deeply nested data model rejected.
	deep := strings.Repeat(`{"a":`, 60) + `1` + strings.Repeat(`}`, 60)
	var val any
	if err := json.Unmarshal([]byte(deep), &val); err != nil {
		t.Skipf("json layer rejected nesting: %v", err)
	}
	envs, err := DecodeEnvelopes([]byte(`[{"version":"v1.0","createSurface":{"surfaceId":"d"}},{"version":"v1.0","updateDataModel":{"surfaceId":"d","path":"/x","value":` + deep + `}}]`))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	errs := e.Apply(context.Background(), envs)
	if len(errs) == 0 {
		t.Error("deep data model should error")
	} else if !errors.Is(errs[len(errs)-1].Err, ErrDataModelTooDeep) {
		t.Errorf("want ErrDataModelTooDeep, got %v", errs)
	}
	// Too many components.
	e2 := NewEngine()
	comps := make([]string, 0, MaxComponentsPerSurface+10)
	for i := 0; i < MaxComponentsPerSurface+10; i++ {
		comps = append(comps, `{"id":"c`+string(rune('a'+i%26))+string(rune('a'+(i/26)%26))+string(rune('a'+(i/676)%26))+iotaSuffix(i)+`","component":"Text","text":"x"}`)
	}
	raw := `[{"version":"v1.0","createSurface":{"surfaceId":"s","components":[` + strings.Join(comps, ",") + `]}}]`
	envs, err = DecodeEnvelopes([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	errs = e2.Apply(context.Background(), envs)
	if len(errs) == 0 || !errors.Is(errs[0].Err, ErrTooManyComponents) {
		t.Errorf("component cap: errs = %v", errs)
	}
	// Cancellation between envelopes.
	e3 := NewEngine()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	errs = e3.Apply(ctx, []Envelope{
		{Version: VersionV1, Key: keyCreateSurface, CreateSurface: &CreateSurface{SurfaceID: "s"}},
	})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "canceled") {
		t.Errorf("canceled apply errs = %v", errs)
	}
	if e3.Surface("s") != nil {
		t.Error("canceled batch must not create surfaces")
	}
}

func iotaSuffix(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}
