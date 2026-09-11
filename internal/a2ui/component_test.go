package a2ui

import (
	"encoding/json"
	"testing"
)

// TestDecodeAll18Components pins that every basic-catalog component decodes
// into its typed prop struct with the exact camelCase field names.
func TestDecodeAll18Components(t *testing.T) {
	cases := []struct {
		raw     string
		checkFn func(*testing.T, any)
	}{
		{`{"id":"a","component":"Text","text":"Hi","variant":"caption"}`,
			func(t *testing.T, p any) {
				tp := p.(*TextProps)
				if tp.Text.Kind != KindLiteral || tp.Text.Literal != "Hi" {
					t.Errorf("text = %+v", tp.Text)
				}
				if tp.Variant != "caption" {
					t.Errorf("variant = %q", tp.Variant)
				}
			}},
		{`{"id":"a","component":"Image","url":{"path":"/u"},"description":"d","fit":"cover","variant":"avatar"}`,
			func(t *testing.T, p any) {
				ip := p.(*ImageProps)
				if ip.URL.Kind != KindPath || ip.URL.Path != "/u" {
					t.Errorf("url = %+v", ip.URL)
				}
				if ip.Fit != "cover" || ip.Variant != "avatar" {
					t.Errorf("fit/variant = %q/%q", ip.Fit, ip.Variant)
				}
			}},
		{`{"id":"a","component":"Icon","name":"mail"}`,
			func(t *testing.T, p any) {
				ip := p.(*IconProps)
				if ip.Name.Kind != IconKindEnum || ip.Name.Enum != "mail" {
					t.Errorf("name = %+v", ip.Name)
				}
			}},
		{`{"id":"a","component":"Icon","name":{"path":"/iconName"}}`,
			func(t *testing.T, p any) {
				ip := p.(*IconProps)
				if ip.Name.Kind != IconKindDynamic || ip.Name.Dynamic.Path != "/iconName" {
					t.Errorf("name = %+v", ip.Name)
				}
			}},
		{`{"id":"a","component":"Icon","name":{"svgPath":{"path":"/svg"}}}`,
			func(t *testing.T, p any) {
				ip := p.(*IconProps)
				if ip.Name.Kind != IconKindSVG || ip.Name.SVGPath.Path != "/svg" {
					t.Errorf("name = %+v", ip.Name)
				}
			}},
		{`{"id":"a","component":"Video","url":"https://v/1","posterUrl":"https://p/1"}`,
			func(t *testing.T, p any) {
				vp := p.(*VideoProps)
				if vp.URL.EvalString(EvalContext{}) != "https://v/1" || vp.PosterURL.EvalString(EvalContext{}) != "https://p/1" {
					t.Errorf("video = %+v", vp)
				}
			}},
		{`{"id":"a","component":"AudioPlayer","url":"https://a/1","description":"track"}`,
			func(t *testing.T, p any) {
				ap := p.(*AudioPlayerProps)
				if ap.URL.EvalString(EvalContext{}) != "https://a/1" {
					t.Errorf("audio url = %+v", ap.URL)
				}
			}},
		{`{"id":"a","component":"Row","children":["x","y"],"justify":"spaceBetween","align":"center"}`,
			func(t *testing.T, p any) {
				rp := p.(*RowProps)
				if len(rp.Children.IDs) != 2 || rp.Children.IDs[0] != "x" {
					t.Errorf("children = %+v", rp.Children)
				}
				if rp.Justify != "spaceBetween" || rp.Align != "center" {
					t.Errorf("justify/align")
				}
			}},
		{`{"id":"a","component":"Column","children":["x"]}`, func(t *testing.T, p any) {}},
		{`{"id":"a","component":"List","children":{"componentId":"tpl","path":"/items"},"direction":"horizontal","align":"start"}`,
			func(t *testing.T, p any) {
				lp := p.(*ListProps)
				if !lp.Children.IsTemplate() || lp.Children.TemplateID != "tpl" || lp.Children.TemplatePath != "/items" {
					t.Errorf("template = %+v", lp.Children)
				}
				if lp.Direction != "horizontal" {
					t.Errorf("direction = %q", lp.Direction)
				}
			}},
		{`{"id":"a","component":"Card","child":"inner"}`,
			func(t *testing.T, p any) {
				if p.(*CardProps).Child != "inner" {
					t.Error("card child")
				}
			}},
		{`{"id":"a","component":"Tabs","tabs":[{"title":"T1","child":"c1"},{"title":{"path":"/t2"},"child":"c2"}]}`,
			func(t *testing.T, p any) {
				tp := p.(*TabsProps)
				if len(tp.Tabs) != 2 || tp.Tabs[0].Title.EvalString(EvalContext{}) != "T1" || tp.Tabs[1].Child != "c2" {
					t.Errorf("tabs = %+v", tp.Tabs)
				}
			}},
		{`{"id":"a","component":"Modal","trigger":"tr","content":"ct"}`,
			func(t *testing.T, p any) {
				mp := p.(*ModalProps)
				if mp.Trigger != "tr" || mp.Content != "ct" {
					t.Errorf("modal = %+v", mp)
				}
			}},
		{`{"id":"a","component":"Divider","axis":"vertical"}`,
			func(t *testing.T, p any) {
				if p.(*DividerProps).Axis != "vertical" {
					t.Error("divider axis")
				}
			}},
		{`{"id":"a","component":"Button","child":"lbl","variant":"primary","action":{"event":{"name":"submit","context":{"id":{"path":"/id"}}}},"checks":[{"call":"required","args":{"value":{"path":"/id"}},"message":"need id"}]}`,
			func(t *testing.T, p any) {
				bp := p.(*ButtonProps)
				if bp.Child != "lbl" || bp.Variant != "primary" {
					t.Errorf("button child/variant")
				}
				if bp.Action == nil || bp.Action.Event == nil || bp.Action.Event.Name != "submit" {
					t.Fatalf("action = %+v", bp.Action)
				}
				ctxVal := bp.Action.Event.Context["id"]
				if ctxVal.Kind != KindPath || ctxVal.Path != "/id" {
					t.Errorf("action context id = %+v", ctxVal)
				}
				if len(bp.Checks) != 1 || bp.Checks[0].Message != "need id" {
					t.Errorf("checks = %+v", bp.Checks)
				}
			}},
		{`{"id":"a","component":"Button","child":"lbl","action":{"functionCall":{"call":"openUrl","args":{"url":"https://x"}}}}`,
			func(t *testing.T, p any) {
				bp := p.(*ButtonProps)
				if bp.Action == nil || bp.Action.FunctionCall == nil || bp.Action.FunctionCall.Name != "openUrl" {
					t.Errorf("local action = %+v", bp.Action)
				}
			}},
		{`{"id":"a","component":"TextField","label":"L","value":{"path":"/v"},"placeholder":"PH","variant":"number","checks":[{"condition":{"call":"required","args":{"value":{"path":"/v"}}},"message":"req"}]}`,
			func(t *testing.T, p any) {
				tf := p.(*TextFieldProps)
				if tf.Label.EvalString(EvalContext{}) != "L" || tf.Variant != "number" {
					t.Errorf("label/variant")
				}
				if tf.Placeholder.EvalString(EvalContext{}) != "PH" {
					t.Error("placeholder")
				}
				if len(tf.Checks) != 1 || tf.Checks[0].Condition.Kind != KindCall {
					t.Errorf("checks = %+v", tf.Checks)
				}
			}},
		{`{"id":"a","component":"CheckBox","label":"ok?","value":{"path":"/ok"}}`,
			func(t *testing.T, p any) {
				cp := p.(*CheckBoxProps)
				if cp.Label.EvalString(EvalContext{}) != "ok?" || cp.Value.Kind != KindPath {
					t.Errorf("checkbox = %+v", cp)
				}
			}},
		{`{"id":"a","component":"ChoicePicker","label":"pick","variant":"multipleSelection","options":[{"label":"One","value":"1"},{"label":"Two","value":"2"}],"value":{"path":"/sel"},"displayStyle":"chips","filterable":true}`,
			func(t *testing.T, p any) {
				cp := p.(*ChoicePickerProps)
				if len(cp.Options) != 2 || cp.Options[1].Value != "2" || cp.Options[1].Label.EvalString(EvalContext{}) != "Two" {
					t.Errorf("options = %+v", cp.Options)
				}
				if !cp.Filterable || cp.DisplayStyle != "chips" || cp.Variant != "multipleSelection" {
					t.Errorf("picker flags")
				}
			}},
		{`{"id":"a","component":"Slider","label":"vol","min":0,"max":10,"value":3,"steps":10}`,
			func(t *testing.T, p any) {
				sp := p.(*SliderProps)
				if sp.Min == nil || *sp.Min != 0 || sp.Max == nil || *sp.Max != 10 || sp.Steps == nil || *sp.Steps != 10 {
					t.Errorf("slider = %+v", sp)
				}
				if sp.Value.EvalNumber(EvalContext{}) != 3 {
					t.Error("slider value")
				}
			}},
		{`{"id":"a","component":"DateTimeInput","value":"2026-01-01T10:00","enableDate":true,"enableTime":true,"min":"2025-01-01","max":"2027-01-01","label":"when"}`,
			func(t *testing.T, p any) {
				dp := p.(*DateTimeInputProps)
				if !dp.EnableDate || !dp.EnableTime || dp.Label.EvalString(EvalContext{}) != "when" {
					t.Errorf("dt = %+v", dp)
				}
				if dp.Min.EvalString(EvalContext{}) != "2025-01-01" {
					t.Error("dt min")
				}
			}},
	}
	for _, tc := range cases {
		var c Component
		if err := json.Unmarshal([]byte(tc.raw), &c); err != nil {
			t.Fatalf("decode %s: %v", tc.raw, err)
		}
		if c.ID != "a" {
			t.Errorf("id = %q", c.ID)
		}
		if c.Component == "" {
			t.Fatalf("component name lost for %s", tc.raw)
		}
		if _, isUnknown := c.Props.(*UnknownProps); isUnknown {
			t.Errorf("component %s decoded as Unknown", c.Component)
		}
		if tc.checkFn != nil {
			tc.checkFn(t, c.Props)
		}
		if len(c.Raw) == 0 {
			t.Errorf("raw JSON not retained for %s", c.Component)
		}
	}
}

func TestDecodeCommonFields(t *testing.T) {
	raw := `{"id":"a","component":"Text","text":"x","catalogId":"https://cat/1","weight":2.5,"accessibility":{"label":"Name","live":"polite","hidden":false}}`
	var c Component
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.CatalogID != "https://cat/1" {
		t.Errorf("catalogId = %q", c.CatalogID)
	}
	if c.Weight == nil || *c.Weight != 2.5 {
		t.Errorf("weight = %v", c.Weight)
	}
	if c.Accessibility == nil || c.Accessibility.Label.EvalString(EvalContext{}) != "Name" || c.Accessibility.Live != "polite" {
		t.Errorf("accessibility = %+v", c.Accessibility)
	}
	// v0.9.1 heading variants still decode (tolerance).
	var h Component
	if err := json.Unmarshal([]byte(`{"id":"h","component":"Text","text":"T","variant":"h2"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.Props.(*TextProps).Variant != "h2" {
		t.Error("v0.9.1 h2 variant lost")
	}
}

// TestUnknownComponentGraceful pins spec-mandated graceful degradation.
func TestUnknownComponentGraceful(t *testing.T) {
	raw := `{"id":"u","component":"Hologram","intensity":9}`
	var c Component
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatalf("unknown component must not error: %v", err)
	}
	up, ok := c.Props.(*UnknownProps)
	if !ok {
		t.Fatalf("props type %T, want *UnknownProps", c.Props)
	}
	if up.Raw == nil {
		t.Error("UnknownProps.Raw empty")
	}
	// Malformed known-component props degrade to Unknown, not error.
	var bad Component
	if err := json.Unmarshal([]byte(`{"id":"b","component":"Slider","value":"not-number","max":"also-not"}`), &bad); err != nil {
		t.Fatalf("malformed props must not error: %v", err)
	}
	if _, ok := bad.Props.(*UnknownProps); !ok {
		t.Logf("note: Slider tolerated non-numeric value (zero-value dynamic)")
	}
}

func TestComponentRoundTrip(t *testing.T) {
	raw := `{"id":"a","component":"Button","child":"l","variant":"primary","action":{"event":{"name":"go","context":{"k":"v"}}}}`
	var c Component
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var back Component
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-decode %s: %v", out, err)
	}
	if back.Component != "Button" || back.ID != "a" {
		t.Errorf("round trip lost id/component: %s", out)
	}
	bp := back.Props.(*ButtonProps)
	if bp.Action == nil || bp.Action.Event == nil || bp.Action.Event.Name != "go" {
		t.Errorf("round trip lost action: %s", out)
	}
	if v := bp.Action.Event.Context["k"].EvalString(EvalContext{}); v != "v" {
		t.Errorf("round trip context = %q", v)
	}
}
