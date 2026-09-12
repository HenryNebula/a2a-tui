// Package widget implements the interactive A2UI surface model: a focus
// cycle over the surface's input components, two-way binding of edits into
// the surface data model via JSON Pointers, live validation through the
// engine's check evaluation, and button activation producing the
// renderer-to-agent action output.
//
// The widget never imports the TUI (or the agent session); the containing
// pane forwards tea.KeyMsg while its surface is focused and translates the
// returned ActionOut into an outbound A2A message.
//
// Concurrency: like the rest of the single-model Bubble Tea app, Update and
// View run on the UI thread, the same thread chat.SplitMessage applies
// engine envelopes on. Data-model writes additionally go through
// Surface.SetDataModelPath (and reads through Surface.DataModelAtPath), so
// even an embedding that applies engine envelopes off-thread cannot data-
// race with the widget. Refresh re-materializes after the engine mutated
// the surface.
package widget

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// Interaction limits protecting against hostile surfaces.
const (
	// maxFocusables bounds the focus list (a surface can declare up to
	// 10k components; only the reachable inputs matter).
	maxFocusables = 512
	// inputWidth is the default editor width in columns.
	inputWidth = 28
)

// compKind classifies a focusable component.
type compKind int

// Focusable component kinds.
const (
	kindButton compKind = iota
	kindTextField
	kindCheckBox
	kindChoicePicker
	kindSlider
	kindDateTime
)

// focusID is the focus/refresh identity of one interactive component
// instance: the component ID plus the absolute JSON Pointer prefix of the
// template row it was instantiated for (empty outside templates). Every
// materialization of the same row yields the same focusID, while rows of a
// list template (which all carry the template's component ID) stay
// distinct.
type focusID struct {
	id    string
	scope string
}

// focusable is one interactive component instance discovered in the
// materialized tree, plus its editor state.
type focusable struct {
	// id is the component ID (reported outward; not unique across template
	// rows).
	id string
	// key is the unique focus and refresh identity (see focusID).
	key focusID
	// kind selects the editing semantics.
	kind compKind
	// node is the materialized node snapshot the editor renders from.
	node *a2ui.Node
	// path is the absolute JSON Pointer the value binds to ("" when the
	// value is not bound to an absolute path — the component then renders
	// as a read-only preview and is skipped from the focus list).
	path string

	// input is the embedded editor for TextField, DateTimeInput and
	// filterable ChoicePicker filter lines.
	input textinput.Model
	// cursor is the ChoicePicker option cursor (index into Options).
	cursor int
	// dirty marks user edits not yet acknowledged by the agent.
	dirty bool
	// touched marks widgets the user has edited at least once; check
	// hints only render for touched widgets (or after an activation
	// attempt), never on a pristine surface.
	touched bool
	// lastWritten is the last value this widget wrote at path (refresh
	// dirty-tracking: an external write equal to it must not clobber the
	// user's editor).
	lastWritten any
}

// ActionOut reports an interaction that must leave the widget: a button
// fired an agent event (Action) or requested a local function call.
type ActionOut struct {
	// EnvelopeVersion is the surface's A2UI protocol version.
	EnvelopeVersion string
	// Action is the action payload to deliver to the agent (buttons with
	// an {event} action). Nil when no agent round trip is needed.
	Action *a2ui.Action
	// DataModel is the surface data-model snapshot to attach when the
	// surface was created with sendDataModel (nil otherwise).
	DataModel any
	// LocalFunctionCall is the button's {functionCall} action, to be
	// executed locally through the engine's function registry (openUrl is
	// gated by the caller behind user confirmation).
	LocalFunctionCall *a2ui.Call
}

// Model is the interactive state for one A2UI surface.
type Model struct {
	surf  *a2ui.Surface
	funcs *a2ui.FunctionRegistry
	root  *a2ui.Node

	focusables []*focusable
	focused    int

	// dirty reports edits made since the last fired action.
	dirty bool
	// flash is a transient hint shown under the focused component (e.g.
	// "checks failed").
	flash string
	// activateAttempted records that the user tried to activate a button
	// (enter/space on one). Check-failure hints stay hidden on a pristine
	// surface until a value was touched or an activation was attempted.
	activateAttempted bool
}

// New materializes the surface and collects the focusables in display
// (depth-first declaration) order. Buttons whose checks fail remain
// focusable (the spec's disabled button) but refuse to fire.
func New(surface *a2ui.Surface) (*Model, error) {
	if surface == nil {
		return nil, fmt.Errorf("widget: nil surface")
	}
	m := &Model{surf: surface, funcs: a2ui.NewFunctionRegistry()}
	if err := m.rebuild(nil); err != nil {
		return nil, err
	}
	return m, nil
}

// SurfaceID returns the interactive surface's ID.
func (m *Model) SurfaceID() string { return m.surf.ID }

// Dirty reports whether the user edited values since the last fired action.
func (m *Model) Dirty() bool { return m.dirty }

// FocusedID returns the focused component's ID ("" with no focusables).
func (m *Model) FocusedID() string {
	if f := m.current(); f != nil {
		return f.id
	}
	return ""
}

// evalCtx builds the root evaluation context for the surface.
func (m *Model) evalCtx() a2ui.EvalContext {
	return m.surf.EvalContextFor(m.funcs)
}

// rebuild materializes the tree and collects focusables. previous maps
// focus keys to editor state to carry over (nil on first build).
func (m *Model) rebuild(previous map[focusID]*focusable) error {
	root, err := m.surf.Materialize()
	if err != nil {
		return err
	}
	m.root = root
	m.focusables = m.focusables[:0]
	seen := map[focusID]bool{}
	m.collect(root, "", seen, previous)
	// Model-level dirty follows the surviving editors: adopted external
	// values clear it, kept user edits preserve it.
	m.dirty = false
	for _, f := range m.focusables {
		if f.dirty {
			m.dirty = true
			break
		}
	}
	if len(m.focusables) > 0 {
		if m.focused < 0 || m.focused >= len(m.focusables) {
			m.focused = len(m.focusables) - 1
		}
		m.applyFocus()
	}
	return nil
}

// applyFocus syncs the embedded editors with the focused index (all other
// editors blurred).
func (m *Model) applyFocus() {
	for _, f := range m.focusables {
		f.input.Blur()
	}
	if f := m.current(); f != nil && f.usesInput() {
		f.input.Focus()
	}
}

// collect walks the visible tree depth-first, creating focusables for
// interactive components (first occurrence per focus key — shared DAG
// subtrees would otherwise produce competing editors for one component
// instance). scope is the absolute JSON Pointer prefix of the enclosing
// template row ("" outside templates): it distinguishes the rows of a list
// template and turns their relative bindings into writable absolute paths.
func (m *Model) collect(n *a2ui.Node, scope string, seen map[focusID]bool, previous map[focusID]*focusable) {
	if n == nil || n.Placeholder || n.Component == nil || len(m.focusables) >= maxFocusables {
		return
	}
	key := focusID{id: n.Component.ID, scope: scope}
	if !seen[key] {
		seen[key] = true
		if f := m.newFocusable(n, scope); f != nil {
			if old, ok := previous[key]; ok {
				f.adopt(old, m)
			}
			m.focusables = append(m.focusables, f)
		}
	}
	switch props := n.Component.Props.(type) {
	case *a2ui.TabsProps:
		// The view renders only the active (first) tab (see viewRenderer),
		// so focusables in the hidden tabs must not join the focus cycle —
		// Tab would move focus into invisible components and edits would
		// mutate hidden fields.
		if len(n.Children) > 0 {
			m.collect(n.Children[0], scope, seen, previous)
		}
		return
	case *a2ui.RowProps:
		m.collectChildren(n, &props.Children, scope, seen, previous)
		return
	case *a2ui.ColumnProps:
		m.collectChildren(n, &props.Children, scope, seen, previous)
		return
	case *a2ui.ListProps:
		m.collectChildren(n, &props.Children, scope, seen, previous)
		return
	}
	for _, c := range n.Children {
		m.collect(c, scope, seen, previous)
	}
}

// collectChildren descends into a container's child list. Template rows
// each get their own scope — the template list's absolute pointer plus the
// row index, mirroring how Materialize instantiates and evaluates them —
// so per-row focusables stay distinct and writes land in the right row.
func (m *Model) collectChildren(n *a2ui.Node, cl *a2ui.ChildList, scope string, seen map[focusID]bool, previous map[focusID]*focusable) {
	if !cl.IsTemplate() {
		for _, c := range n.Children {
			m.collect(c, scope, seen, previous)
		}
		return
	}
	// Resolve the template's own path the way expandTemplate does: absolute
	// paths address the root model, relative ones the current row scope.
	base := cl.TemplatePath
	if !strings.HasPrefix(base, "/") {
		if scope == "" {
			base = "" // relative at root scope never resolves: rows stay read-only
		} else {
			base = scope + "/" + base
		}
	}
	for _, c := range n.Children {
		row := ""
		if base != "" && c.Index != nil {
			row = base + "/" + strconv.Itoa(*c.Index)
		}
		m.collect(c, row, seen, previous)
	}
}

// newFocusable creates the focusable for one node, or nil when the
// component is not an editable input (static components, or values not
// bound to a writable data-model path: literals, calls, and relative
// bindings outside template rows).
func (m *Model) newFocusable(n *a2ui.Node, scope string) *focusable {
	f := &focusable{id: n.Component.ID, key: focusID{id: n.Component.ID, scope: scope}, node: n}
	switch props := n.Component.Props.(type) {
	case *a2ui.ButtonProps:
		f.kind = kindButton
	case *a2ui.TextFieldProps:
		f.kind = kindTextField
		f.path = bindingPath(props.Value, scope)
		f.input = newEditor(m, n, props.Placeholder.EvalString(n.EvalContext(m.evalCtx())),
			props.Variant == "obscured", fieldBodyW(props.Variant)-1)
	case *a2ui.CheckBoxProps:
		f.kind = kindCheckBox
		f.path = bindingPath(props.Value, scope)
	case *a2ui.ChoicePickerProps:
		f.kind = kindChoicePicker
		f.path = bindingPath(props.Value, scope)
		if props.Filterable {
			f.input = newEditor(m, n, "filter…", false, inputWidth)
		}
	case *a2ui.SliderProps:
		f.kind = kindSlider
		f.path = bindingPath(props.Value, scope)
	case *a2ui.DateTimeInputProps:
		f.kind = kindDateTime
		f.path = bindingPath(props.Value, scope)
		f.input = newEditor(m, n, dateTimePlaceholder(props.EnableDate, props.EnableTime), false, dateBodyW-1)
	default:
		return nil
	}
	// Inputs need a writable binding; buttons never bind.
	if f.kind != kindButton && f.path == "" {
		return nil
	}
	return f
}

// newEditor builds the embedded text input seeded from the bound value,
// rendering width cells wide (see fieldBodyW: the editor pads its view to
// Width cells plus one cursor cell, matching the unfocused bracket
// interior). An empty placeholder renders a blank editor (no synthetic
// word). The cursor carries an explicit contrast style — a reverse block
// in the accent color — so the insertion point stays visible even on
// terminals where a bare reverse space disappears.
func newEditor(m *Model, n *a2ui.Node, placeholder string, obscured bool, width int) textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.Width = width
	ti.CharLimit = 0
	ti.Placeholder = placeholder
	ti.Cursor.Style = lipgloss.NewStyle().Reverse(true).
		Foreground(lipgloss.Color("231")).
		Background(lipgloss.Color("176"))
	if obscured {
		ti.EchoMode = textinput.EchoPassword
		ti.EchoCharacter = '•'
	}
	ti.SetValue(currentString(m, n))
	return ti
}

// bindingPath returns the absolute JSON Pointer a value binding writes to.
// Absolute paths pass through; relative paths — legal only inside template
// rows, where they address the row element — compose against the row's
// scope prefix. Literals, calls, empty paths and scopeless relatives are
// read-only and return "".
func bindingPath(d a2ui.Dynamic, scope string) string {
	if d.Kind != a2ui.KindPath || d.Path == "" {
		return ""
	}
	if strings.HasPrefix(d.Path, "/") {
		return d.Path
	}
	if scope == "" {
		return ""
	}
	return scope + "/" + d.Path
}

// currentString evaluates a node's bound value as a display string.
func currentString(m *Model, n *a2ui.Node) string {
	var d a2ui.Dynamic
	switch props := n.Component.Props.(type) {
	case *a2ui.TextFieldProps:
		d = props.Value
	case *a2ui.DateTimeInputProps:
		d = props.Value
	default:
		return ""
	}
	return d.EvalString(n.EvalContext(m.evalCtx()))
}

// dateTimePlaceholder builds the ISO 8601 hint from the enabled fields.
func dateTimePlaceholder(date, timeEnabled bool) string {
	switch {
	case date && timeEnabled:
		return "YYYY-MM-DDTHH:MM"
	case date:
		return "YYYY-MM-DD"
	case timeEnabled:
		return "HH:MM"
	default:
		return "ISO 8601"
	}
}

// adopt carries editor state from a previous incarnation of the same
// component across a Refresh, honoring the dirty-tracking rule: keep the
// user's value while the model still holds what we last wrote; adopt the
// external value once it differs.
func (f *focusable) adopt(old *focusable, m *Model) {
	f.cursor = old.cursor
	f.dirty = old.dirty
	f.touched = old.touched
	f.lastWritten = old.lastWritten
	if f.kind != old.kind {
		return // component changed type: start fresh
	}
	keep := false
	switch f.kind {
	case kindTextField, kindDateTime:
		keep = old.dirty && f.path != "" && valuesEqual(boundValue(m.surf, f.path), old.lastWritten)
	case kindChoicePicker:
		// Stateless editor (the model is the value), but keep the filter
		// line so an agent update mid-filter does not reset it.
		f.input = old.input
		return
	case kindSlider, kindCheckBox:
		// Stateless editors read the model on every keypress; nothing to
		// carry except the dirty bookkeeping.
		return
	}
	if keep {
		f.input = old.input
	} else {
		f.dirty = false
		f.lastWritten = nil
	}
}

// boundValue reads the value at path from the surface data model (under
// the surface's read lock).
func boundValue(surf *a2ui.Surface, path string) any {
	if path == "" {
		return nil
	}
	v, _ := surf.DataModelAtPath(path)
	return v
}

// valuesEqual compares two plain JSON values.
func valuesEqual(a, b any) bool {
	return reflect.DeepEqual(a, b)
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

// Update processes one message while this surface is focused. It mutates
// the model in place, reporting whether an action must leave the widget,
// whether the key was consumed (unconsumed keys fall through to the pane's
// viewport scrolling), and any binding error.
func (m *Model) Update(msg tea.Msg) (ActionOut, bool, error) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return ActionOut{}, false, nil
	}
	switch key.String() {
	case "tab":
		m.focusNext(1)
		return ActionOut{}, true, nil
	case "shift+tab":
		m.focusNext(-1)
		return ActionOut{}, true, nil
	}
	f := m.current()
	if f == nil {
		return ActionOut{}, false, nil
	}
	return m.updateFocused(f, key)
}

// current returns the focused focusable (nil when the surface has none).
func (m *Model) current() *focusable {
	if len(m.focusables) == 0 {
		return nil
	}
	if m.focused < 0 || m.focused >= len(m.focusables) {
		m.focused = 0
	}
	return m.focusables[m.focused]
}

// focusNext moves focus by delta, wrapping around.
func (m *Model) focusNext(delta int) {
	m.flash = ""
	if len(m.focusables) == 0 {
		return
	}
	m.focused = ((m.focused+delta)%len(m.focusables) + len(m.focusables)) % len(m.focusables)
	m.applyFocus()
}

// usesInput reports whether the focusable's embedded editor takes keys.
func (f *focusable) usesInput() bool {
	switch f.kind {
	case kindTextField, kindDateTime:
		return true
	case kindChoicePicker:
		return f.picker().Filterable
	}
	return false
}

// updateFocused dispatches one key to the focused component's editing
// semantics.
func (m *Model) updateFocused(f *focusable, key tea.KeyMsg) (ActionOut, bool, error) {
	switch f.kind {
	case kindButton:
		return m.updateButton(f, key.String())
	case kindTextField:
		return m.updateTextField(f, key)
	case kindCheckBox:
		switch key.String() {
		case " ", "enter":
			next := !truthy(boundValue(m.surf, f.path))
			return ActionOut{}, true, m.setValue(f, next)
		}
		return ActionOut{}, false, nil
	case kindChoicePicker:
		return m.updatePicker(f, key)
	case kindSlider:
		return m.updateSlider(f, key.String())
	case kindDateTime:
		return m.updateDateTime(f, key)
	}
	return ActionOut{}, false, nil
}

// updateTextField forwards editing keys to the embedded input and writes
// the value through on every change.
func (m *Model) updateTextField(f *focusable, key tea.KeyMsg) (ActionOut, bool, error) {
	switch key.String() {
	case "enter":
		// Enter commits (the write-through already happened per keypress)
		// and advances to the next field — standard form flow.
		m.focusNext(1)
		return ActionOut{}, true, nil
	case "up", "down":
		return ActionOut{}, false, nil // let the pane viewport scroll
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(key)
	_ = cmd // cursor blink commands are dropped; the cursor stays solid
	var err error
	if variant, ok := f.textFieldVariant(); ok && variant == "number" {
		err = m.writeNumber(f)
	} else {
		err = m.setValue(f, f.input.Value())
	}
	return ActionOut{}, true, err
}

// textFieldVariant returns the TextField variant of the focusable.
func (f *focusable) textFieldVariant() (string, bool) {
	if props, ok := f.node.Component.Props.(*a2ui.TextFieldProps); ok {
		return props.Variant, true
	}
	return "", false
}

// writeNumber writes a numeric field: empty deletes the key, a parseable
// value writes a JSON number, anything else leaves the model untouched
// (the view shows the format hint).
func (m *Model) writeNumber(f *focusable) error {
	raw := strings.TrimSpace(f.input.Value())
	if raw == "" {
		return m.setValue(f, nil) // jsonptr.Set maps nil to key deletion
	}
	n, err := parseNumber(raw)
	if err != nil {
		return nil // invalid: keep the model clean, hint in the view
	}
	return m.setValue(f, n)
}

// updateDateTime edits an ISO 8601 input, writing only parseable values.
func (m *Model) updateDateTime(f *focusable, key tea.KeyMsg) (ActionOut, bool, error) {
	switch key.String() {
	case "enter":
		m.focusNext(1)
		return ActionOut{}, true, nil
	case "up", "down":
		return ActionOut{}, false, nil
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(key)
	_ = cmd
	raw := strings.TrimSpace(f.input.Value())
	var err error
	switch {
	case raw == "":
		err = m.setValue(f, nil)
	case parseDateTime(raw) == nil:
		// Unparseable input stays local until corrected; the view hints.
	default:
		err = m.setValue(f, raw)
	}
	return ActionOut{}, true, err
}

// updatePicker handles option cursor movement, selection and the optional
// filter line.
func (m *Model) updatePicker(f *focusable, key tea.KeyMsg) (ActionOut, bool, error) {
	props := f.picker()
	s := key.String()
	visible := f.visibleOptions(m)
	switch s {
	case "up":
		if f.cursor > 0 {
			f.cursor--
		}
		return ActionOut{}, true, nil
	case "down":
		if f.cursor < len(visible)-1 {
			f.cursor++
		}
		return ActionOut{}, true, nil
	case " ", "enter":
		if len(visible) == 0 {
			return ActionOut{}, true, nil
		}
		idx := f.cursor
		if idx >= len(visible) {
			idx = len(visible) - 1
		}
		f.cursor = idx
		opt := visible[idx]
		if props.Variant == "multipleSelection" {
			return ActionOut{}, true, m.toggleMulti(f, opt)
		}
		return ActionOut{}, true, m.setValue(f, opt.Value)
	}
	if props.Filterable {
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(key)
		_ = cmd
		return ActionOut{}, true, nil
	}
	return ActionOut{}, false, nil
}

// visibleOptions returns the picker options matching the current filter
// (all of them when the picker is not filterable).
func (f *focusable) visibleOptions(m *Model) []a2ui.ChoiceOption {
	props := f.picker()
	if !props.Filterable {
		return props.Options
	}
	ctx := f.node.EvalContext(m.evalCtx())
	needle := strings.ToLower(f.input.Value())
	out := make([]a2ui.ChoiceOption, 0, len(props.Options))
	for _, opt := range props.Options {
		if needle == "" || strings.Contains(strings.ToLower(opt.Label.EvalString(ctx)), needle) {
			out = append(out, opt)
		}
	}
	return out
}

// picker returns the ChoicePicker props of the focusable.
func (f *focusable) picker() *a2ui.ChoicePickerProps {
	props, _ := f.node.Component.Props.(*a2ui.ChoicePickerProps)
	return props
}

// toggleMulti toggles one value in a multipleSelection picker, writing the
// new selection in option order.
func (m *Model) toggleMulti(f *focusable, opt a2ui.ChoiceOption) error {
	props := f.picker()
	selected := map[string]bool{}
	for _, v := range selectedList(m, f) {
		selected[v] = true
	}
	selected[opt.Value] = !selected[opt.Value]
	out := make([]any, 0, len(props.Options))
	for _, o := range props.Options {
		if selected[o.Value] {
			out = append(out, o.Value)
		}
	}
	return m.setValue(f, out)
}

// selectedList reads the picker's currently selected values.
func selectedList(m *Model, f *focusable) []string {
	props := f.picker()
	return props.Value.EvalStringList(f.node.EvalContext(m.evalCtx()))
}

// updateSlider adjusts the value by one step (left/right) or 10% of the
// range (up/down), snapping to the step grid when the agent declared steps.
func (m *Model) updateSlider(f *focusable, s string) (ActionOut, bool, error) {
	props, ok := f.node.Component.Props.(*a2ui.SliderProps)
	if !ok {
		return ActionOut{}, false, nil
	}
	min, max := 0.0, 100.0
	if props.Min != nil {
		min = *props.Min
	}
	if props.Max != nil {
		max = *props.Max
	}
	if max <= min {
		max = min + 1
	}
	step := 1.0
	if props.Steps != nil && *props.Steps >= 1 {
		step = (max - min) / float64(*props.Steps)
	}
	cur := props.Value.EvalNumber(f.node.EvalContext(m.evalCtx()))
	switch s {
	case "left":
		cur -= step
	case "right":
		cur += step
	case "up":
		cur += (max - min) * 0.1
	case "down":
		cur -= (max - min) * 0.1
	default:
		return ActionOut{}, false, nil
	}
	if cur < min {
		cur = min
	}
	if cur > max {
		cur = max
	}
	if props.Steps != nil && *props.Steps >= 1 && step > 0 {
		// Snap to the nearest grid point so repeated steps stay exact.
		cur = min + mathRound((cur-min)/step)*step
		if cur < min {
			cur = min
		}
		if cur > max {
			cur = max
		}
	}
	return ActionOut{}, true, m.setValue(f, cur)
}

// updateButton activates the focused button. A button whose own checks
// fail never fires (spec intent); the failing messages flash under it.
func (m *Model) updateButton(f *focusable, s string) (ActionOut, bool, error) {
	if s != "enter" && s != " " {
		return ActionOut{}, false, nil
	}
	// The user tried to act: from now on failing checks may show their
	// hints (a pristine surface renders no validation errors).
	m.activateAttempted = true
	props, ok := f.node.Component.Props.(*a2ui.ButtonProps)
	if !ok {
		return ActionOut{}, true, nil
	}
	ctx := f.node.EvalContext(m.evalCtx())
	for _, rule := range props.Checks {
		if res := a2ui.EvalCheck(rule, ctx); !res.Valid() {
			m.flash = "checks failed: " + checkMessage(res, rule)
			return ActionOut{}, true, nil
		}
	}
	if props.Action == nil {
		return ActionOut{}, true, nil
	}
	out := ActionOut{EnvelopeVersion: m.version()}
	switch {
	case props.Action.Event != nil:
		ev := props.Action.Event
		context := map[string]any{}
		for k, d := range ev.Context {
			v, err := d.Evaluate(ctx)
			if err != nil {
				return ActionOut{}, true, err
			}
			context[k] = v
		}
		rm := a2ui.NewActionMessage(m.version(), m.surf.ID, f.id, ev.Name, context)
		out.Action = rm.Action
		if msg := ev.UserMessage.EvalString(ctx); msg != "" {
			out.Action.UserMessage = msg
		}
		if m.surf.SendDataModel {
			out.DataModel = m.surf.DataModel
		}
		// The action carries the edits out; a refresh now defers to the
		// agent's echo.
		m.dirty = false
		for _, fo := range m.focusables {
			fo.dirty = false
		}
	case props.Action.FunctionCall != nil:
		out.LocalFunctionCall = props.Action.FunctionCall
	}
	return out, true, nil
}

// version returns the surface's A2UI protocol version (v1.0 default).
func (m *Model) version() string {
	if m.surf.Version != "" {
		return m.surf.Version
	}
	return a2ui.VersionV1
}

// setValue writes val at the focusable's bound path (two-way binding) so
// bound texts and the static renderer observe the edit immediately.
//
// Concurrency: the write goes through Surface.SetDataModelPath, the
// exported locked write path, so it serializes with engine-side envelope
// application and concurrent renderers reading snapshots. (The embedding
// contract in the package doc — Update on the UI goroutine — remains the
// primary ordering guarantee; the lock removes the data race for
// embeddings that apply envelopes off-thread.)
func (m *Model) setValue(f *focusable, val any) error {
	if f.path == "" {
		return nil
	}
	if err := m.surf.SetDataModelPath(f.path, val); err != nil {
		return fmt.Errorf("widget: bind %s: %w", f.path, err)
	}
	f.dirty = true
	f.touched = true
	f.lastWritten = val
	m.dirty = true
	return nil
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

// Refresh re-materializes the surface after the engine applied
// updateComponents/updateDataModel envelopes. Focus is preserved by
// component ID, and per-focusable dirty-tracking keeps user-edited editor
// values while the data model still holds the value the widget last wrote
// (an external overwrite is adopted instead).
func (m *Model) Refresh() error {
	previous := make(map[focusID]*focusable, len(m.focusables))
	focusedKey := focusID{}
	hadFocus := false
	for i, f := range m.focusables {
		previous[f.key] = f
		if i == m.focused {
			focusedKey = f.key
			hadFocus = true
		}
	}
	if err := m.rebuild(previous); err != nil {
		return err
	}
	if hadFocus {
		for i, f := range m.focusables {
			if f.key == focusedKey {
				m.focused = i
				break
			}
		}
	}
	m.applyFocus()
	return nil
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

// truthy coerces a bound value to bool.
func truthy(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

// parseNumber parses a JSON-style number.
func parseNumber(raw string) (float64, error) {
	f, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("widget: not a number: %q", raw)
	}
	return f, nil
}

// dateTimeLayouts are the accepted ISO 8601 input layouts.
var dateTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04",
	"2006-01-02",
	"15:04",
}

// parseDateTime parses an ISO 8601 value, returning nil when unparseable.
func parseDateTime(raw string) *time.Time {
	for _, layout := range dateTimeLayouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return &ts
		}
	}
	return nil
}

// mathRound rounds half away from zero.
func mathRound(f float64) float64 {
	if f < 0 {
		return -mathRound(-f)
	}
	return float64(int64(f + 0.5))
}
