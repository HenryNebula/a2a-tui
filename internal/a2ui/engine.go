package a2ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/HenryNebula/a2a-tui/internal/a2ui/jsonptr"
)

// BasicCatalogIDV1 is the catalogId of the v1.0 basic catalog.
const BasicCatalogIDV1 = "https://a2ui.org/specification/v1_0/catalogs/basic/catalog.json"

// BasicCatalogIDV091 is the catalogId of the v0.9.1 basic catalog.
const BasicCatalogIDV091 = "https://a2ui.org/specification/v0_9_1/catalogs/basic/catalog.json"

// MimeTypeA2UI is the DataPart metadata mimeType marking A2UI payloads.
const MimeTypeA2UI = "application/a2ui+json"

// ApplyError captures a per-envelope failure. Processing is explicitly
// non-transactional: the A2A extension spec requires sequential processing
// with per-message error reporting, so later envelopes still apply.
type ApplyError struct {
	// Index is the position of the failing envelope in the batch.
	Index int
	// Kind names the payload ("createSurface", ...).
	Kind string
	// SurfaceID is the surface involved, when known.
	SurfaceID string
	// Err is the underlying error.
	Err error
}

// Error implements error.
func (e ApplyError) Error() string {
	loc := fmt.Sprintf("envelope %d (%s)", e.Index, e.Kind)
	if e.SurfaceID != "" {
		loc += " surface " + e.SurfaceID
	}
	return loc + ": " + e.Err.Error()
}

// Unwrap exposes the underlying error.
func (e ApplyError) Unwrap() error { return e.Err }

// Error codes used by ApplyError.Err for structural failures.
var (
	// ErrSurfaceExists: createSurface on an existing surfaceId.
	ErrSurfaceExists = fmt.Errorf("a2ui: surface already exists")
	// ErrNoSurface: message for a surface that was never created.
	ErrNoSurface = fmt.Errorf("a2ui: surface does not exist")
	// ErrTooManyComponents: component cap exceeded.
	ErrTooManyComponents = fmt.Errorf("a2ui: too many components")
	// ErrDataModelTooDeep: data model deeper than MaxDepth.
	ErrDataModelTooDeep = fmt.Errorf("a2ui: data model too deep")
)

// ChangeSet summarizes which surfaces a batch touched.
type ChangeSet struct {
	// Created lists surface IDs created by the batch.
	Created []string
	// Updated lists surface IDs mutated by the batch.
	Updated []string
	// Deleted lists surface IDs removed by the batch.
	Deleted []string
}

// Engine maintains renderer-side surface state and applies envelopes.
// It is safe for concurrent use.
type Engine struct {
	mu       sync.Mutex
	surfaces map[string]*Surface
	funcs    *FunctionRegistry
	// onUpdate, when set, receives the change set after each Apply.
	onUpdate func(ChangeSet)
}

// EngineOption configures an Engine.
type EngineOption func(*Engine)

// WithFunctions replaces the engine's function registry.
func WithFunctions(r *FunctionRegistry) EngineOption {
	return func(e *Engine) { e.funcs = r }
}

// WithUpdateCallback registers a callback invoked (outside the engine lock)
// after every Apply that changed something.
func WithUpdateCallback(fn func(ChangeSet)) EngineOption {
	return func(e *Engine) { e.onUpdate = fn }
}

// NewEngine creates an engine with the basic-catalog functions registered.
func NewEngine(opts ...EngineOption) *Engine {
	e := &Engine{
		surfaces: make(map[string]*Surface),
		funcs:    NewFunctionRegistry(),
	}
	for _, opt := range opts {
		opt(e)
	}
	if e.funcs == nil {
		e.funcs = NewFunctionRegistry()
	}
	return e
}

// Functions exposes the engine's function registry (for registering extras).
func (e *Engine) Functions() *FunctionRegistry { return e.funcs }

// Surface returns a snapshot pointer to a live surface, or nil.
func (e *Engine) Surface(id string) *Surface {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.surfaces[id]
}

// SurfaceIDs lists live surface IDs in stable order.
func (e *Engine) SurfaceIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.surfaces))
	for id := range e.surfaces {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Apply processes envelopes sequentially, capturing per-envelope errors.
// Unknown protocol versions are processed best-effort and recorded as
// ApplyErrors carrying ErrUnknownVersion; the message still applies.
func (e *Engine) Apply(ctx context.Context, envelopes []Envelope) []ApplyError {
	var errs []ApplyError
	cs := ChangeSet{}
	touched := map[string]bool{}
	created := map[string]bool{}
	deleted := map[string]bool{}

	for i := range envelopes {
		if ctx.Err() != nil {
			errs = append(errs, ApplyError{
				Index: i, Kind: string(envelopes[i].Key),
				Err: fmt.Errorf("a2ui: canceled: %w", ctx.Err()),
			})
			break
		}
		env := envelopes[i]
		if err := env.Validate(); err != nil {
			errs = append(errs, ApplyError{Index: i, Kind: string(env.Key), Err: err})
		}
		kind := string(env.Key)
		var sid string
		var err error
		switch {
		case env.CreateSurface != nil:
			sid = env.CreateSurface.SurfaceID
			err = e.applyCreate(env.CreateSurface, env.Version)
			if err == nil {
				created[sid] = true
			}
		case env.UpdateComponents != nil:
			sid = env.UpdateComponents.SurfaceID
			err = e.applyUpdateComponents(env.UpdateComponents)
		case env.UpdateDataModel != nil:
			sid = env.UpdateDataModel.SurfaceID
			err = e.applyUpdateDataModel(env.UpdateDataModel)
		case env.DeleteSurface != nil:
			sid = env.DeleteSurface.SurfaceID
			err = e.applyDelete(env.DeleteSurface)
			if err == nil {
				deleted[sid] = true
			}
		case env.CallRendererFunction != nil:
			// Executing agent-initiated renderer functions requires the
			// interactive layer (and a reply channel); the static engine
			// records the request and continues.
			err = fmt.Errorf("a2ui: callRendererFunction %q received; static engine does not execute renderer functions",
				env.CallRendererFunction.CallFunction.Name)
		case env.AgentFunctionResponse != nil:
			// Responses to renderer-initiated calls are consumed by the
			// interactive layer; nothing to mutate here.
			err = nil
		default:
			err = fmt.Errorf("a2ui: envelope has no payload")
		}
		if err != nil {
			errs = append(errs, ApplyError{Index: i, Kind: kind, SurfaceID: sid, Err: err})
			continue
		}
		if sid != "" && !deleted[sid] {
			touched[sid] = true
		}
	}

	for id := range created {
		cs.Created = append(cs.Created, id)
	}
	for id := range touched {
		if !created[id] {
			cs.Updated = append(cs.Updated, id)
		}
	}
	sort.Strings(cs.Created)
	sort.Strings(cs.Updated)
	for id := range deleted {
		cs.Deleted = append(cs.Deleted, id)
	}
	sort.Strings(cs.Deleted)

	if e.onUpdate != nil && (len(cs.Created)+len(cs.Updated)+len(cs.Deleted)) > 0 {
		e.onUpdate(cs)
	}
	return errs
}

func (e *Engine) applyCreate(msg *CreateSurface, version string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.surfaces[msg.SurfaceID]; exists {
		return fmt.Errorf("%w: %s", ErrSurfaceExists, msg.SurfaceID)
	}
	if len(msg.Components) > MaxComponentsPerSurface {
		return fmt.Errorf("%w: %d in createSurface", ErrTooManyComponents, len(msg.Components))
	}
	s := NewSurface(msg.SurfaceID)
	s.mu.Lock()
	s.CatalogID = msg.CatalogID
	s.Version = version
	s.SendDataModel = msg.SendDataModel
	for i := range msg.Components {
		comp := msg.Components[i]
		if comp.ID == "" {
			continue
		}
		s.Components[comp.ID] = &comp
	}
	if msg.DataModel != nil {
		if !jsonptrDepthOK(msg.DataModel, 0) {
			s.mu.Unlock()
			return fmt.Errorf("%w: createSurface.dataModel", ErrDataModelTooDeep)
		}
		s.DataModel = msg.DataModel
	}
	s.mu.Unlock()
	e.surfaces[msg.SurfaceID] = s
	return nil
}

func (e *Engine) applyUpdateComponents(msg *UpdateComponents) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.surfaces[msg.SurfaceID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSurface, msg.SurfaceID)
	}
	if len(s.Components)+len(msg.Components) > MaxComponentsPerSurface {
		return fmt.Errorf("%w: %d total", ErrTooManyComponents, len(s.Components)+len(msg.Components))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range msg.Components {
		comp := msg.Components[i]
		if comp.ID == "" {
			continue // a component without an ID can never be referenced
		}
		s.Components[comp.ID] = &comp
	}
	s.UpdatedAt = time.Now()
	return nil
}

func (e *Engine) applyUpdateDataModel(msg *UpdateDataModel) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.surfaces[msg.SurfaceID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSurface, msg.SurfaceID)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.Value != nil && !jsonptrDepthOK(msg.Value, 0) {
		return fmt.Errorf("%w: updateDataModel.value", ErrDataModelTooDeep)
	}
	// A2UI semantics: an omitted path OR "/" addresses the entire data
	// model (unlike raw RFC 6901, where "/" is the empty key).
	path := msg.Path
	wholeModel := path == "" || path == "/"
	var next any
	if s.DataModel != nil {
		next = s.DataModel
	}
	if msg.Value == nil {
		// null (v1.0) or omitted (v0.9.1) deletes the key at path.
		if wholeModel {
			next = nil
		} else {
			var removed bool
			var err error
			if next, removed, err = jsonptr.Delete(next, path); err != nil {
				return fmt.Errorf("a2ui: delete at %s: %w", path, err)
			}
			_ = removed // deleting a missing key is a no-op (upsert semantics)
		}
	} else if wholeModel {
		next = msg.Value
	} else {
		var err error
		if next, err = jsonptr.Set(next, path, msg.Value); err != nil {
			return fmt.Errorf("a2ui: set at %s: %w", path, err)
		}
	}
	s.DataModel = next
	s.UpdatedAt = time.Now()
	return nil
}

func (e *Engine) applyDelete(msg *DeleteSurface) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.surfaces[msg.SurfaceID]; !ok {
		return fmt.Errorf("%w: %s", ErrNoSurface, msg.SurfaceID)
	}
	delete(e.surfaces, msg.SurfaceID)
	return nil
}

// OutboundCapabilities builds the metadata map a renderer should attach to
// every outbound message: the v1.0 renderer capabilities and the v0.9.1-era
// client capabilities (both shapes are sent so agents of either generation
// can negotiate).
func (e *Engine) OutboundCapabilities() map[string]any {
	return map[string]any{
		"a2uiRendererCapabilities": map[string]any{
			VersionV1: map[string]any{
				"supportedCatalogIds": []string{BasicCatalogIDV1},
			},
		},
		"a2uiClientCapabilities": map[string]any{
			VersionV09: map[string]any{
				"supportedCatalogIds": []string{BasicCatalogIDV091},
			},
		},
	}
}

// DataModelMetadata builds the a2uiRendererDataModel metadata attachment for
// the surfaces created with sendDataModel=true. With no IDs passed, every
// syncing surface is included. It returns nil when nothing qualifies (the
// attachment is then omitted entirely).
func (e *Engine) DataModelMetadata(surfaceIDs ...string) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := surfaceIDs
	if len(ids) == 0 {
		for id, s := range e.surfaces {
			if s.SendDataModel {
				ids = append(ids, id)
			}
		}
	}
	surfaces := map[string]any{}
	for _, id := range ids {
		s, ok := e.surfaces[id]
		if !ok || !s.SendDataModel {
			continue
		}
		model := s.DataModel
		if model == nil {
			model = map[string]any{}
		}
		if m, ok := model.(map[string]any); ok {
			surfaces[id] = m
		} else {
			// Schema requires object values; wrap non-object models.
			surfaces[id] = map[string]any{"value": model}
		}
	}
	if len(surfaces) == 0 {
		return nil
	}
	return map[string]any{
		"version":  VersionV1,
		"surfaces": surfaces,
	}
}

// ExtractEnvelopes detects an A2A DataPart carrying A2UI (metadata mimeType
// application/a2ui+json) and decodes its data value (an array of envelopes,
// tolerating a single object) into envelopes. The bool result reports
// whether the part is A2UI at all; decode failures return the envelopes
// decoded so far plus an error.
func ExtractEnvelopes(dataPartValue any, metadata map[string]any) ([]Envelope, bool) {
	if !isA2UIMetadata(metadata) {
		return nil, false
	}
	raw, err := json.Marshal(dataPartValue)
	if err != nil || len(raw) == 0 {
		return nil, true
	}
	envs, err := DecodeEnvelopes(raw)
	if err != nil {
		if envs == nil {
			envs = nil
		}
		return envs, true
	}
	return envs, true
}

// isA2UIMetadata checks the mimeType marker on a decoded metadata map.
func isA2UIMetadata(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	mt, ok := metadata["mimeType"].(string)
	return ok && mt == MimeTypeA2UI
}
