package a2ui

import (
	"encoding/json"
	"fmt"
)

// AccessibilityAttributes per common_types.json: label, description, live and
// hidden.
type Accessibility struct {
	Label       Dynamic `json:"label,omitempty"`
	Description Dynamic `json:"description,omitempty"`
	Live        string  `json:"live,omitempty"`
	Hidden      Dynamic `json:"hidden,omitempty"`
}

// ChildList is the children property of containers: either a static array of
// component IDs or a {componentId, path} template object that instantiates
// the referenced component once per element of the data-model list at path.
type ChildList struct {
	// IDs holds a static child-ID array when Kind is static.
	IDs []string
	// TemplateID and TemplatePath hold the {componentId, path} template form.
	TemplateID   string
	TemplatePath string
}

// IsTemplate reports whether the child list uses the template form.
func (c ChildList) IsTemplate() bool { return c.TemplateID != "" }

// UnmarshalJSON accepts both wire forms.
func (c *ChildList) UnmarshalJSON(data []byte) error {
	var ids []string
	if err := json.Unmarshal(data, &ids); err == nil {
		c.IDs = ids
		return nil
	}
	var tpl struct {
		ComponentID string `json:"componentId"`
		Path        string `json:"path"`
	}
	if err := json.Unmarshal(data, &tpl); err != nil {
		return fmt.Errorf("a2ui: children must be an ID array or {componentId, path}: %w", err)
	}
	c.TemplateID, c.TemplatePath = tpl.ComponentID, tpl.Path
	return nil
}

// MarshalJSON re-encodes either wire form.
func (c ChildList) MarshalJSON() ([]byte, error) {
	if c.IsTemplate() {
		return json.Marshal(map[string]string{"componentId": c.TemplateID, "path": c.TemplatePath})
	}
	return json.Marshal(c.IDs)
}

// ActionEvent triggers an agent-side event from an interaction.
type ActionEvent struct {
	Name        string             `json:"name"`
	UserMessage Dynamic            `json:"userMessage,omitempty"`
	Context     map[string]Dynamic `json:"context,omitempty"`
}

// ActionSpec is the action property of Button: an agent event or a local
// function call (common_types.json Action, exactly one of the two).
type ActionSpec struct {
	Event        *ActionEvent
	FunctionCall *Call
}

// UnmarshalJSON decodes the oneOf {event} | {functionCall} shape.
func (a *ActionSpec) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if raw, ok := probe["event"]; ok {
		var ev ActionEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			return err
		}
		a.Event = &ev
		return nil
	}
	if raw, ok := probe["functionCall"]; ok {
		var c Call
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		a.FunctionCall = &c
		return nil
	}
	return fmt.Errorf("a2ui: action must contain event or functionCall")
}

// MarshalJSON re-encodes the action.
func (a ActionSpec) MarshalJSON() ([]byte, error) {
	if a.Event != nil {
		return json.Marshal(map[string]any{"event": a.Event})
	}
	if a.FunctionCall != nil {
		return json.Marshal(map[string]any{"functionCall": a.FunctionCall})
	}
	return []byte("null"), nil
}

// CheckRule is a validation rule: a condition (path or function call that
// yields a boolean in v0.9.1 or a ValidationResult in v1.0) plus an optional
// fallback message. The spec's worked examples also send bare function calls
// with a sibling "message"; that shape is tolerated.
type CheckRule struct {
	Condition Dynamic
	Message   string
}

// UnmarshalJSON accepts {"condition": {...}, "message": "..."} and the bare
// {"call": ..., "args": ..., "message": "..."} form.
func (r *CheckRule) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	rawCond, hasCond := probe["condition"]
	if rawMsg, ok := probe["message"]; ok {
		_ = json.Unmarshal(rawMsg, &r.Message)
	}
	if !hasCond {
		// Bare form: the whole object minus "message" is the condition.
		delete(probe, "message")
		remap, err := json.Marshal(probe)
		if err != nil {
			return err
		}
		rawCond = remap
	}
	if err := r.Condition.UnmarshalJSON(rawCond); err != nil {
		return fmt.Errorf("a2ui: check condition: %w", err)
	}
	return nil
}

// MarshalJSON re-encodes using the schema-strict shape.
func (r CheckRule) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"condition": r.Condition, "message": r.Message})
}

// Checkable groups the checks property shared by Button and all input
// components.
type Checkable struct {
	Checks []CheckRule `json:"checks,omitempty"`
}

// IconName is the Icon name property: one of the catalog's Material icon
// enum names, an {svgPath: ...} object, or a dynamic binding.
type IconName struct {
	// Enum holds the string enum value when Kind is Enum.
	Enum string
	// SVGPath holds the decoded svgPath when Kind is SVG.
	SVGPath Dynamic
	// Dynamic holds the whole property when it is a path/call binding.
	Dynamic Dynamic
	// Kind distinguishes the three shapes.
	Kind IconNameKind
}

// IconNameKind classifies an IconName.
type IconNameKind int

// Icon name shapes.
const (
	IconKindEnum IconNameKind = iota
	IconKindSVG
	IconKindDynamic
)

// UnmarshalJSON sniffs the three allowed shapes.
func (n *IconName) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		n.Kind, n.Enum = IconKindEnum, s
		return nil
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if raw, ok := probe["svgPath"]; ok {
		n.Kind = IconKindSVG
		return json.Unmarshal(raw, &n.SVGPath)
	}
	if _, ok := probe["path"]; ok {
		n.Kind = IconKindDynamic
		return n.Dynamic.UnmarshalJSON(data)
	}
	if _, ok := probe["call"]; ok {
		n.Kind = IconKindDynamic
		return n.Dynamic.UnmarshalJSON(data)
	}
	return fmt.Errorf("a2ui: icon name must be an enum string, {svgPath} or a binding")
}

// MarshalJSON re-encodes the icon name.
func (n IconName) MarshalJSON() ([]byte, error) {
	switch n.Kind {
	case IconKindSVG:
		return json.Marshal(map[string]any{"svgPath": n.SVGPath})
	case IconKindDynamic:
		return json.Marshal(n.Dynamic)
	default:
		return json.Marshal(n.Enum)
	}
}

// UnknownProps carries the raw JSON of a component whose name is not in the
// catalog. Graceful degradation is spec-mandated: unknown components render
// as placeholders, never errors.
type UnknownProps struct {
	Raw json.RawMessage
}

// Typed prop structs, one per basic-catalog component. Field names and JSON
// tags follow the catalog's camelCase property names exactly.

// TextProps is the Text component: {text, variant}.
type TextProps struct {
	Text    Dynamic `json:"text"`
	Variant string  `json:"variant,omitempty"`
}

// ImageProps is the Image component: {url, description, fit, variant}.
type ImageProps struct {
	URL         Dynamic `json:"url"`
	Description Dynamic `json:"description,omitempty"`
	Fit         string  `json:"fit,omitempty"`
	Variant     string  `json:"variant,omitempty"`
}

// IconProps is the Icon component: {name}.
type IconProps struct {
	Name IconName `json:"name"`
}

// VideoProps is the Video component: {url, posterUrl}.
type VideoProps struct {
	URL       Dynamic `json:"url"`
	PosterURL Dynamic `json:"posterUrl,omitempty"`
}

// AudioPlayerProps is the AudioPlayer component: {url, description}.
type AudioPlayerProps struct {
	URL         Dynamic `json:"url"`
	Description Dynamic `json:"description,omitempty"`
}

// RowProps is the Row component: {children, justify, align}.
type RowProps struct {
	Children ChildList `json:"children"`
	Justify  string    `json:"justify,omitempty"`
	Align    string    `json:"align,omitempty"`
}

// ColumnProps is the Column component: {children, justify, align}.
type ColumnProps struct {
	Children ChildList `json:"children"`
	Justify  string    `json:"justify,omitempty"`
	Align    string    `json:"align,omitempty"`
}

// ListProps is the List component: {children, direction, align}.
type ListProps struct {
	Children  ChildList `json:"children"`
	Direction string    `json:"direction,omitempty"`
	Align     string    `json:"align,omitempty"`
}

// CardProps is the Card component: {child}.
type CardProps struct {
	Child string `json:"child"`
}

// TabSpec is one entry of Tabs.tabs: {title, child}.
type TabSpec struct {
	Title Dynamic `json:"title"`
	Child string  `json:"child"`
}

// TabsProps is the Tabs component: {tabs: [{title, child}]}.
type TabsProps struct {
	Tabs []TabSpec `json:"tabs"`
}

// ModalProps is the Modal component: {trigger, content}.
type ModalProps struct {
	Trigger string `json:"trigger"`
	Content string `json:"content"`
}

// DividerProps is the Divider component: {axis}.
type DividerProps struct {
	Axis string `json:"axis,omitempty"`
}

// ButtonProps is the Button component: {child, variant, action, checks}.
type ButtonProps struct {
	Checkable
	Child   string      `json:"child"`
	Variant string      `json:"variant,omitempty"`
	Action  *ActionSpec `json:"action,omitempty"`
}

// TextFieldProps is the TextField component:
// {label, value, placeholder, variant, checks}.
type TextFieldProps struct {
	Checkable
	Label       Dynamic `json:"label"`
	Value       Dynamic `json:"value,omitempty"`
	Placeholder Dynamic `json:"placeholder,omitempty"`
	Variant     string  `json:"variant,omitempty"`
}

// CheckBoxProps is the CheckBox component: {label, value, checks}.
type CheckBoxProps struct {
	Checkable
	Label Dynamic `json:"label"`
	Value Dynamic `json:"value"`
}

// ChoiceOption is one entry of ChoicePicker.options: {label, value}.
type ChoiceOption struct {
	Label Dynamic `json:"label"`
	Value string  `json:"value"`
}

// ChoicePickerProps is the ChoicePicker component:
// {label, variant, options, value, displayStyle, filterable, checks}.
type ChoicePickerProps struct {
	Checkable
	Label        Dynamic        `json:"label,omitempty"`
	Variant      string         `json:"variant,omitempty"`
	Options      []ChoiceOption `json:"options"`
	Value        Dynamic        `json:"value"`
	DisplayStyle string         `json:"displayStyle,omitempty"`
	Filterable   bool           `json:"filterable,omitempty"`
}

// SliderProps is the Slider component:
// {label, min, max, value, steps, checks}.
type SliderProps struct {
	Checkable
	Label Dynamic  `json:"label,omitempty"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	Value Dynamic  `json:"value"`
	Steps *int     `json:"steps,omitempty"`
}

// DateTimeInputProps is the DateTimeInput component:
// {value, enableDate, enableTime, min, max, label, checks}.
type DateTimeInputProps struct {
	Checkable
	Value      Dynamic `json:"value"`
	EnableDate bool    `json:"enableDate,omitempty"`
	EnableTime bool    `json:"enableTime,omitempty"`
	Min        Dynamic `json:"min,omitempty"`
	Max        Dynamic `json:"max,omitempty"`
	Label      Dynamic `json:"label,omitempty"`
}

// Component is one entry of a surface's flat component list: the envelope
// common fields (id, component, catalogId, accessibility, weight) plus the
// decoded typed props for the component's name. The original JSON is kept on
// Raw for debugging and for unknown-component fallbacks.
type Component struct {
	// ID is the surface-unique component identifier.
	ID string
	// Component is the component type name ("Text", "Row", ...).
	Component string
	// CatalogID overrides the surface default catalog for this component.
	CatalogID string
	// Accessibility holds the optional accessibility attributes.
	Accessibility *Accessibility
	// Weight is the optional flex-grow weight inside a Row or Column.
	Weight *float64
	// Props is the decoded typed prop struct (or *UnknownProps).
	Props any
	// Raw is the component's original JSON.
	Raw json.RawMessage
}

// componentEnvelope decodes the common fields; remaining keys are left in the
// raw JSON for the typed decoder.
type componentEnvelope struct {
	ID            string         `json:"id"`
	Component     string         `json:"component"`
	CatalogID     string         `json:"catalogId"`
	Accessibility *Accessibility `json:"accessibility"`
	Weight        *float64       `json:"weight"`
}

// UnmarshalJSON decodes common fields and dispatches typed prop decoding by
// component name. Unknown names decode to UnknownProps without error.
func (c *Component) UnmarshalJSON(data []byte) error {
	var env componentEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	c.ID, c.Component, c.CatalogID = env.ID, env.Component, env.CatalogID
	c.Accessibility, c.Weight = env.Accessibility, env.Weight
	c.Raw = append(json.RawMessage(nil), data...)

	strip := func(keys ...string) json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(data, &m); err != nil {
			return data
		}
		for _, k := range keys {
			delete(m, k)
		}
		out, err := json.Marshal(m)
		if err != nil {
			return data
		}
		return out
	}
	propsOnly := strip("id", "component", "catalogId", "accessibility", "weight")

	var props any
	var err error
	switch c.Component {
	case "Text":
		p := &TextProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Image":
		p := &ImageProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Icon":
		p := &IconProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Video":
		p := &VideoProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "AudioPlayer":
		p := &AudioPlayerProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Row":
		p := &RowProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Column":
		p := &ColumnProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "List":
		p := &ListProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Card":
		p := &CardProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Tabs":
		p := &TabsProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Modal":
		p := &ModalProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Divider":
		p := &DividerProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Button":
		p := &ButtonProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "TextField":
		p := &TextFieldProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "CheckBox":
		p := &CheckBoxProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "ChoicePicker":
		p := &ChoicePickerProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "Slider":
		p := &SliderProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "DateTimeInput":
		p := &DateTimeInputProps{}
		err = json.Unmarshal(propsOnly, p)
		props = p
	case "":
		err = fmt.Errorf("a2ui: component object is missing the component name")
	default:
		props = &UnknownProps{Raw: append(json.RawMessage(nil), data...)}
	}
	if err != nil {
		// Malformed props degrade to Unknown with the raw JSON preserved;
		// the rest of the surface keeps rendering.
		props = &UnknownProps{Raw: append(json.RawMessage(nil), data...)}
	}
	c.Props = props
	return nil
}

// MarshalJSON re-encodes the component from its typed representation by
// merging common fields with the props object.
func (c Component) MarshalJSON() ([]byte, error) {
	if c.Component == "" {
		return c.Raw, nil
	}
	base := map[string]any{
		"id":        c.ID,
		"component": c.Component,
	}
	if c.CatalogID != "" {
		base["catalogId"] = c.CatalogID
	}
	if c.Accessibility != nil {
		base["accessibility"] = c.Accessibility
	}
	if c.Weight != nil {
		base["weight"] = *c.Weight
	}
	propsJSON, err := json.Marshal(c.Props)
	if err != nil {
		return nil, err
	}
	var props map[string]any
	if err := json.Unmarshal(propsJSON, &props); err != nil {
		return nil, err
	}
	for k, v := range props {
		base[k] = v
	}
	return json.Marshal(base)
}

// BasicCatalogComponentNames lists the 18 basic-catalog component names in
// catalog order.
var BasicCatalogComponentNames = []string{
	"Text", "Image", "Icon", "Video", "AudioPlayer",
	"Row", "Column", "List", "Card", "Tabs", "Modal", "Divider",
	"Button", "TextField", "CheckBox", "ChoicePicker", "Slider", "DateTimeInput",
}
