package a2ui

import (
	"encoding/json"
	"errors"
	"testing"
)

func mustEnvelope(t *testing.T, raw string) Envelope {
	t.Helper()
	var e Envelope
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	return e
}

func TestEnvelopeDecodeShapes(t *testing.T) {
	e := mustEnvelope(t, `{"version":"v1.0","createSurface":{"surfaceId":"s","catalogId":"c","sendDataModel":true,"components":[{"id":"root","component":"Text","text":"hi"}],"dataModel":{"a":1}}}`)
	if e.Version != VersionV1 || e.Key != keyCreateSurface || e.CreateSurface == nil {
		t.Fatalf("decode: %+v", e)
	}
	cs := e.CreateSurface
	if cs.SurfaceID != "s" || cs.CatalogID != "c" || !cs.SendDataModel || len(cs.Components) != 1 {
		t.Errorf("createSurface = %+v", cs)
	}
	if m, ok := cs.DataModel.(map[string]any); !ok || m["a"] != float64(1) {
		t.Errorf("dataModel = %#v", cs.DataModel)
	}

	e = mustEnvelope(t, `{"version":"v1.0","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Row","children":["a"]}]}}`)
	if e.UpdateComponents == nil || len(e.UpdateComponents.Components) != 1 {
		t.Error("updateComponents")
	}

	e = mustEnvelope(t, `{"version":"v1.0","updateDataModel":{"surfaceId":"s","path":"/x","value":null}}`)
	if e.UpdateDataModel == nil || e.UpdateDataModel.Path != "/x" || e.UpdateDataModel.Value != nil {
		t.Errorf("updateDataModel = %+v", e.UpdateDataModel)
	}
	if !e.UpdateDataModel.HasValue {
		t.Error("explicit null must set HasValue")
	}

	e = mustEnvelope(t, `{"version":"v0.9.1","updateDataModel":{"surfaceId":"s","path":"/x"}}`)
	if e.UpdateDataModel.HasValue {
		t.Error("v0.9.1 omitted value must not set HasValue")
	}

	e = mustEnvelope(t, `{"version":"v1.0","deleteSurface":{"surfaceId":"s"}}`)
	if e.DeleteSurface == nil || e.DeleteSurface.SurfaceID != "s" {
		t.Error("deleteSurface")
	}

	e = mustEnvelope(t, `{"version":"v1.0","callRendererFunction":{"functionCallId":"f1","callFunction":{"call":"getScreenResolution","catalogId":"c","args":{"screenIndex":0}}}}`)
	if e.CallRendererFunction == nil || e.CallRendererFunction.CallFunction.Name != "getScreenResolution" {
		t.Errorf("callRendererFunction = %+v", e.CallRendererFunction)
	}

	e = mustEnvelope(t, `{"version":"v1.0","agentFunctionResponse":{"functionCallId":"f2","value":[1920,1080]}}`)
	if e.AgentFunctionResponse == nil || e.AgentFunctionResponse.FunctionCallID != "f2" {
		t.Errorf("agentFunctionResponse = %+v", e.AgentFunctionResponse)
	}
}

func TestEnvelopeValidation(t *testing.T) {
	bad := []string{
		`{}`,
		`{"version":"v1.0"}`,
		`{"createSurface":{"surfaceId":"s"}}`, // no version
		`{"version":"v1.0","createSurface":{"surfaceId":"s"},"deleteSurface":{"surfaceId":"s"}}`, // two payloads
		`{"version":"v1.0","updateComponents":{"surfaceId":"s"}}`,                                // missing components
	}
	for _, raw := range bad {
		var e Envelope
		if err := json.Unmarshal([]byte(raw), &e); err == nil {
			t.Errorf("%s should fail validation", raw)
		}
	}
	// Unknown version decodes (best-effort) but Validate reports it.
	e := mustEnvelope(t, `{"version":"v1.1","createSurface":{"surfaceId":"s"}}`)
	if err := e.Validate(); !errors.Is(err, ErrUnknownVersion) {
		t.Errorf("Validate err = %v, want ErrUnknownVersion", err)
	}
	// Known versions validate.
	for _, v := range []string{VersionV1, VersionV091, VersionV09} {
		e := mustEnvelope(t, `{"version":"`+v+`","createSurface":{"surfaceId":"s"}}`)
		if err := e.Validate(); err != nil {
			t.Errorf("version %s: %v", v, err)
		}
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	raws := []string{
		`{"version":"v1.0","createSurface":{"surfaceId":"s","catalogId":"c"}}`,
		`{"version":"v1.0","updateComponents":{"surfaceId":"s","components":[{"id":"root","component":"Text","text":"hi"}]}}`,
		`{"version":"v1.0","updateDataModel":{"surfaceId":"s","path":"/a/b","value":[1,2]}}`,
		`{"version":"v1.0","deleteSurface":{"surfaceId":"s"}}`,
		`{"version":"v1.0","callRendererFunction":{"functionCallId":"f","callFunction":{"call":"openUrl","catalogId":"c","args":{"url":"https://x"}}}}`,
		`{"version":"v1.0","agentFunctionResponse":{"functionCallId":"f","value":true}}`,
	}
	for _, raw := range raws {
		e := mustEnvelope(t, raw)
		out, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal %s: %v", raw, err)
		}
		var back Envelope
		if err := json.Unmarshal(out, &back); err != nil {
			t.Fatalf("re-decode %s: %v", out, err)
		}
		if back.Key != e.Key || back.Version != e.Version {
			t.Errorf("round trip %s -> %s: key/version changed", raw, out)
		}
	}
}

func TestDecodeEnvelopes(t *testing.T) {
	envs, err := DecodeEnvelopes([]byte(`[{"version":"v1.0","createSurface":{"surfaceId":"a"}},{"version":"v1.0","deleteSurface":{"surfaceId":"a"}}]`))
	if err != nil || len(envs) != 2 {
		t.Fatalf("list decode: %v len=%d", err, len(envs))
	}
	// Single object tolerance.
	envs, err = DecodeEnvelopes([]byte(`{"version":"v1.0","createSurface":{"surfaceId":"a"}}`))
	if err != nil || len(envs) != 1 {
		t.Fatalf("single decode: %v len=%d", err, len(envs))
	}
	if _, err := DecodeEnvelopes([]byte(`[{"version":"v1.0"}]`)); err == nil {
		t.Error("invalid envelope in list must error")
	}
}

func TestRendererMessageRoundTrip(t *testing.T) {
	// action
	rm := NewActionMessage(VersionV1, "surf", "btn", "submit", map[string]any{"k": "v"})
	out, err := json.Marshal(rm)
	if err != nil {
		t.Fatal(err)
	}
	var back RendererMessage
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-decode %s: %v", out, err)
	}
	if back.Action == nil || back.Action.Name != "submit" || back.Action.SurfaceID != "surf" ||
		back.Action.SourceComponentID != "btn" || back.Action.Context["k"] != "v" {
		t.Errorf("action round trip: %s", out)
	}
	if back.Action.Timestamp == "" {
		t.Error("timestamp missing")
	}
	// callAgentFunction
	rm = RendererMessage{Version: VersionV1, CallAgentFunction: &CallAgentFunctionMsg{
		SurfaceID: "s", FunctionCallID: "f1", CallFunction: Call{Name: "verify", Args: map[string]any{"id": "P1"}},
	}}
	out, _ = json.Marshal(rm)
	back = RendererMessage{}
	if err := json.Unmarshal(out, &back); err != nil || back.CallAgentFunction == nil || back.CallAgentFunction.CallFunction.Name != "verify" {
		t.Fatalf("callAgentFunction round trip: %s %v", out, err)
	}
	// rendererFunctionResponse and error
	rm = RendererMessage{Version: VersionV1, RendererFunctionResponse: &RendererFunctionResponseMsg{}}
	rm.RendererFunctionResponse.FunctionCallID = "f2"
	rm.RendererFunctionResponse.Value = []any{1920, 1080}
	out, _ = json.Marshal(rm)
	back = RendererMessage{}
	if err := json.Unmarshal(out, &back); err != nil || back.RendererFunctionResponse == nil {
		t.Fatalf("rendererFunctionResponse: %s %v", out, err)
	}
	rm = RendererMessage{Version: VersionV1, Error: &ErrorPayload{Code: "INVALID_FUNCTION_CALL", Message: "no", FunctionCallID: "f3"}}
	out, _ = json.Marshal(rm)
	back = RendererMessage{}
	if err := json.Unmarshal(out, &back); err != nil || back.Error == nil || back.Error.Code != "INVALID_FUNCTION_CALL" {
		t.Fatalf("error: %s %v", out, err)
	}
	// Exactly-one-payload validation.
	if err := json.Unmarshal([]byte(`{"version":"v1.0","action":{"name":"n","surfaceId":"s","sourceComponentId":"c","timestamp":"t","context":{}},"error":{"code":"x","message":"y"}}`), &back); err == nil {
		t.Error("two payloads should fail")
	}
}
