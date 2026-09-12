// A2UI surface construction for the fixture agent. Surfaces are built with
// the typed internal/a2ui structs and serialized as raw envelope JSON: the
// a2ui.Envelope type validates its exactly-one-payload rule at decode time
// and cannot be assembled outside the package, so the fixture emits the
// wire form directly and tests round-trip it through a2ui.DecodeEnvelopes.
package fixtureagent

import (
	"encoding/json"
	"fmt"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/a2aproject/a2a-go/v2/a2a"
)

// Surface-ID prefixes; the task ID suffix keeps consecutive demos on
// different tasks independent while a createSurface→update pair on the same
// task stays addressable.
const (
	formSurfaceIDPrefix    = "form-"
	dynamicSurfaceIDPrefix = "dynamic-"
	listSurfaceIDPrefix    = "list-"
)

func formSurfaceID(tid a2a.TaskID) string    { return formSurfaceIDPrefix + string(tid) }
func dynamicSurfaceID(tid a2a.TaskID) string { return dynamicSurfaceIDPrefix + string(tid) }
func listSurfaceID(tid a2a.TaskID) string    { return listSurfaceIDPrefix + string(tid) }

// Dynamic-value helpers.

func lit(v any) a2ui.Dynamic {
	return a2ui.Dynamic{Kind: a2ui.KindLiteral, Literal: v}
}

func bind(path string) a2ui.Dynamic {
	return a2ui.Dynamic{Kind: a2ui.KindPath, Path: path}
}

// fmtCall builds a formatString call whose template interpolates ${...}
// data paths and function calls.
func fmtCall(template string) a2ui.Dynamic {
	return a2ui.Dynamic{Kind: a2ui.KindCall, Call: &a2ui.Call{
		Name: "formatString",
		Args: map[string]any{"value": template},
	}}
}

// requiredCheck builds a "required" validation rule on a data-model path.
func requiredCheck(path, message string) a2ui.CheckRule {
	return a2ui.CheckRule{
		Condition: a2ui.Dynamic{Kind: a2ui.KindCall, Call: &a2ui.Call{
			Name: "required",
			Args: map[string]any{"value": map[string]any{"path": path}},
		}},
		Message: message,
	}
}

// Component helpers.

func textComponent(id, text, variant string) a2ui.Component {
	return a2ui.Component{ID: id, Component: "Text", Props: &a2ui.TextProps{
		Text:    lit(text),
		Variant: variant,
	}}
}

func column(id string, children ...string) a2ui.Component {
	return a2ui.Component{ID: id, Component: "Column", Props: &a2ui.ColumnProps{
		Children: a2ui.ChildList{IDs: children},
	}}
}

func textField(id, label, path, variant, placeholder string, checks ...a2ui.CheckRule) a2ui.Component {
	props := &a2ui.TextFieldProps{
		Checkable:   a2ui.Checkable{Checks: checks},
		Label:       lit(label),
		Value:       bind(path),
		Variant:     variant,
		Placeholder: lit(placeholder),
	}
	return a2ui.Component{ID: id, Component: "TextField", Props: props}
}

func choiceOption(label, value string) a2ui.ChoiceOption {
	return a2ui.ChoiceOption{Label: lit(label), Value: value}
}

// formSurface builds the "a2ui form" contact form. It exercises every
// interactive component (TextField in all three variants, CheckBox,
// ChoicePicker in both variants, Slider, DateTimeInput), a Button with an
// event action, a formatString-bound preview text and an initial data model.
func formSurface(tid a2a.TaskID) *a2ui.CreateSurface {
	return &a2ui.CreateSurface{
		SurfaceID: formSurfaceID(tid),
		CatalogID: a2ui.BasicCatalogIDV1,
		Components: []a2ui.Component{
			column("root", "form-title",
				"name-field", "bio-field", "age-field",
				"subscribe-check", "plan-picker", "interests-picker",
				"volume-slider", "meeting-at",
				"name-preview", "submit-btn"),
			textComponent("form-title", "Contact the fixture agent", "h3"),
			textField("name-field", "Name", "/name", "shortText", "", requiredCheck("/name", "Name is required")),
			textField("bio-field", "Bio", "/bio", "longText", "Tell us about yourself"),
			textField("age-field", "Age", "/age", "number", ""),
			{ID: "subscribe-check", Component: "CheckBox", Props: &a2ui.CheckBoxProps{
				Label: lit("Subscribe to updates"),
				Value: bind("/subscribe"),
			}},
			{ID: "plan-picker", Component: "ChoicePicker", Props: &a2ui.ChoicePickerProps{
				Label:   lit("Plan"),
				Variant: "singleSelection",
				Options: []a2ui.ChoiceOption{
					choiceOption("Free", "free"),
					choiceOption("Basic", "basic"),
					choiceOption("Pro", "pro"),
				},
				Value: bind("/plan"),
			}},
			{ID: "interests-picker", Component: "ChoicePicker", Props: &a2ui.ChoicePickerProps{
				Label:      lit("Interests"),
				Variant:    "multipleSelection",
				Filterable: true,
				Options: []a2ui.ChoiceOption{
					choiceOption("A2A", "a2a"),
					choiceOption("A2UI", "a2ui"),
					choiceOption("Terminals", "tui"),
				},
				Value: bind("/interests"),
			}},
			{ID: "volume-slider", Component: "Slider", Props: &a2ui.SliderProps{
				Label: lit("Volume"),
				Min:   ptr(0.0),
				Max:   ptr(11.0),
				Steps: ptr(11),
				Value: bind("/volume"),
			}},
			{ID: "meeting-at", Component: "DateTimeInput", Props: &a2ui.DateTimeInputProps{
				Label:      lit("Preferred meeting"),
				Value:      bind("/meetingAt"),
				EnableDate: true,
				EnableTime: true,
			}},
			{ID: "name-preview", Component: "Text", Props: &a2ui.TextProps{
				Text: fmtCall("Hello, ${/name}!"),
			}},
			{ID: "submit-btn", Component: "Button", Props: &a2ui.ButtonProps{
				Child:   "submit-btn-label",
				Variant: "primary",
				Action:  &a2ui.ActionSpec{Event: &a2ui.ActionEvent{Name: "submit"}},
			}},
			textComponent("submit-btn-label", "Submit", "body"),
		},
		DataModel: map[string]any{
			"name":      "there",
			"bio":       "",
			"age":       nil,
			"subscribe": true,
			"plan":      "pro",
			"interests": []any{"a2a"},
			"volume":    7,
			"meetingAt": "2026-10-01T09:00",
		},
	}
}

// dynamicSurface builds the "a2ui dynamic" demo: a text bound through
// formatString to data-model fields and a button whose action makes the
// agent update those fields (live-update demo).
func dynamicSurface(tid a2a.TaskID) *a2ui.CreateSurface {
	return &a2ui.CreateSurface{
		SurfaceID: dynamicSurfaceID(tid),
		CatalogID: a2ui.BasicCatalogIDV1,
		Components: []a2ui.Component{
			column("root", "counter-text", "refresh-btn"),
			{ID: "counter-text", Component: "Text", Props: &a2ui.TextProps{
				Text: fmtCall("count=${/count}, label=${/label}"),
			}},
			{ID: "refresh-btn", Component: "Button", Props: &a2ui.ButtonProps{
				Child:  "refresh-btn-label",
				Action: &a2ui.ActionSpec{Event: &a2ui.ActionEvent{Name: "refresh"}},
			}},
			textComponent("refresh-btn-label", "Refresh", "body"),
		},
		DataModel: map[string]any{"count": 1, "label": "initial"},
	}
}

// listSurface builds the "a2ui list" demo: a List whose children use the
// {componentId, path} template form over a five-element data array.
func listSurface(tid a2a.TaskID) *a2ui.CreateSurface {
	items := make([]any, 0, 5)
	for _, label := range []string{"echo", "stream", "artifact", "push", "a2ui"} {
		items = append(items, map[string]any{"label": label})
	}
	return &a2ui.CreateSurface{
		SurfaceID: listSurfaceID(tid),
		CatalogID: a2ui.BasicCatalogIDV1,
		Components: []a2ui.Component{
			column("root", "list-title", "demo-list"),
			textComponent("list-title", "Five fixture items", "h3"),
			{ID: "demo-list", Component: "List", Props: &a2ui.ListProps{
				Children: a2ui.ChildList{
					TemplateID:   "list-item",
					TemplatePath: "/items",
				},
				Direction: "vertical",
			}},
			// The per-element template: @index() and the element's "label"
			// field both resolve from the template scope.
			{ID: "list-item", Component: "Text", Props: &a2ui.TextProps{
				Text: fmtCall("${@index()}. ${label}"),
			}},
		},
		DataModel: map[string]any{"items": items},
	}
}

// Envelope wire builders. A2UI rides in an A2A data part whose metadata
// marks mimeType application/a2ui+json and whose value is an array of
// envelopes.

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("fixtureagent: marshal a2ui envelope: %v", err))
	}
	return raw
}

func createSurfaceEnvelope(cs *a2ui.CreateSurface) json.RawMessage {
	return mustJSON(map[string]any{"version": a2ui.VersionV1, "createSurface": cs})
}

// updateDataModelEnvelope with an empty path replaces the whole data model.
func updateDataModelEnvelope(surfaceID, path string, value any) json.RawMessage {
	payload := map[string]any{"surfaceId": surfaceID, "value": value}
	if path != "" {
		payload["path"] = path
	}
	return mustJSON(map[string]any{"version": a2ui.VersionV1, "updateDataModel": payload})
}

func updateComponentsEnvelope(surfaceID string, components ...a2ui.Component) json.RawMessage {
	return mustJSON(map[string]any{
		"version":          a2ui.VersionV1,
		"updateComponents": map[string]any{"surfaceId": surfaceID, "components": components},
	})
}

// a2uiReply emits the envelope batch as one data part. On a taskless
// execution (the A2UI norm) it rides in a terminal agent message scoped to
// the context; when the turn continues an existing task, where the SDK
// forbids message events, it is folded into a completing status message.
func a2uiReply(x *execution, envelopes ...json.RawMessage) bool {
	part := a2a.NewDataPart([]json.RawMessage(envelopes))
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	if x.ec.StoredTask != nil {
		return x.statusWithPart(a2a.TaskStateCompleted, part)
	}
	return x.contextMessage(part)
}

// actionTurn is an inbound renderer→agent message plus the data model the
// renderer attached to it (a2uiRendererDataModel metadata), when present.
type actionTurn struct {
	action *a2ui.Action
	model  map[string]any // surfaces map: surfaceID → data model
}

// contextString reads action.Context[key], falling back to def.
func (t *actionTurn) contextString(key, def string) string {
	if v, ok := t.action.Context[key].(string); ok && v != "" {
		return v
	}
	return def
}

// modelCount reads a numeric field of the attached data model for the
// surface, falling back to def.
func (t *actionTurn) modelCount(surfaceID, key string, def float64) float64 {
	surface, ok := t.model[surfaceID].(map[string]any)
	if !ok {
		return def
	}
	if v, ok := surface[key].(float64); ok {
		return v
	}
	return def
}

// findAction detects a renderer A2UI action in the message: a data part
// marked application/a2ui+json (with a lenient fallback sniff for unmarked
// data parts) that decodes as a renderer message with an action payload.
func findAction(msg *a2a.Message) *actionTurn {
	if msg == nil {
		return nil
	}
	turn := scanParts(msg, true)
	if turn == nil {
		turn = scanParts(msg, false)
	}
	return turn
}

func scanParts(msg *a2a.Message, marked bool) *actionTurn {
	for _, p := range msg.Parts {
		if p.Data() == nil {
			continue
		}
		if marked != isA2UIPart(p.Metadata) {
			continue
		}
		raw, err := json.Marshal(p.Data())
		if err != nil {
			continue
		}
		var rm a2ui.RendererMessage
		if err := json.Unmarshal(raw, &rm); err != nil || rm.Action == nil {
			continue
		}
		return &actionTurn{action: rm.Action, model: attachedSurfaces(msg.Metadata)}
	}
	return nil
}

// isA2UIPart reports whether a part's metadata carries the A2UI mimeType.
func isA2UIPart(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	mt, _ := metadata["mimeType"].(string)
	return mt == a2ui.MimeTypeA2UI
}

// attachedSurfaces extracts the surfaces map from a2uiRendererDataModel
// message metadata ({version, surfaces: {id: model}}), when present.
func attachedSurfaces(metadata map[string]any) map[string]any {
	attachment, ok := metadata["a2uiRendererDataModel"].(map[string]any)
	if !ok {
		return nil
	}
	surfaces, _ := attachment["surfaces"].(map[string]any)
	return surfaces
}

func ptr[T any](v T) *T { return &v }
