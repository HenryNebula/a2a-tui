package a2ui

import (
	"fmt"
	"sync"
	"time"

	"github.com/HenryNebula/a2a-tui/internal/a2ui/jsonptr"
)

// Hard limits protecting materialization from hostile component graphs.
const (
	// MaxComponentsPerSurface bounds the size of a surface's adjacency list.
	MaxComponentsPerSurface = 10000
	// MaxTreeDepth bounds materialized node depth (also the cycle backstop).
	MaxTreeDepth = 64
	// MaxTemplateItems bounds list-template expansion per template.
	MaxTemplateItems = 1000
	// MaxTreeNodes bounds the total materialized node count.
	MaxTreeNodes = 50000
)

// ErrNoRoot is returned by Materialize when the surface has no component
// with ID "root" yet (progressive rendering: other components buffer).
var ErrNoRoot = fmt.Errorf("a2ui: surface has no root component")

// Surface is the renderer-side state for one A2UI surface: the flat
// component adjacency list plus the data model. Mutation happens only
// through Engine.Apply (which upserts whole Component values and replaces
// the data-model root via spine copies, never mutating in place); the
// internal RWMutex lets renderers read snapshots concurrently.
type Surface struct {
	// mu guards Components and DataModel for concurrent readers.
	mu sync.RWMutex
	// ID is the surface identifier from createSurface.
	ID string
	// CatalogID is the surface's default catalog.
	CatalogID string
	// Version is the protocol version that created the surface.
	Version string
	// SendDataModel mirrors createSurface.sendDataModel.
	SendDataModel bool
	// Components is the flat adjacency list keyed by component ID.
	Components map[string]*Component
	// DataModel is the surface data model (decoded JSON tree, or nil).
	DataModel any
	// CreatedAt and UpdatedAt bookkeep surface lifetime.
	CreatedAt, UpdatedAt time.Time
}

// NewSurface allocates an empty surface.
func NewSurface(id string) *Surface {
	now := time.Now()
	return &Surface{
		ID:         id,
		Components: make(map[string]*Component),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

// SetDataModelPath applies one JSON-Pointer write to the data model under
// the surface lock, storing the new root that jsonptr.Set's spine copy
// produces. It is the locked write path for cross-package editors (the
// interactive widget); engine-side updates keep going through the engine,
// whose own lock serializes envelope application against these writes.
func (s *Surface) SetDataModelPath(path string, val any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next, err := jsonptr.Set(s.DataModel, path, val)
	if err != nil {
		return err
	}
	s.DataModel = next
	s.UpdatedAt = time.Now()
	return nil
}

// DataModelAtPath reads the value at a JSON Pointer under the surface's
// read lock (the locked counterpart of SetDataModelPath).
func (s *Surface) DataModelAtPath(path string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return jsonptr.Get(s.DataModel, path)
}

// EvalContextFor builds the root-scope evaluation context for the surface.
func (s *Surface) EvalContextFor(funcs *FunctionRegistry) EvalContext {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return EvalContext{Root: s.DataModel, Funcs: funcs}
}

// nodeBudget tracks node-count limits during materialization.
type nodeBudget struct{ used int }

func (b *nodeBudget) take() error {
	b.used++
	if b.used > MaxTreeNodes {
		return fmt.Errorf("a2ui: materialized tree exceeds %d nodes", MaxTreeNodes)
	}
	return nil
}

// Node is one node of the materialized display tree. Scope and Index carry
// the collection scope established by list-template instantiation so later
// dynamic evaluation resolves relative paths against the right element.
type Node struct {
	// Component is the component definition; nil for placeholders.
	Component *Component
	// Placeholder marks unresolved component references (spec-mandated
	// progressive rendering).
	Placeholder bool
	// RefID is the referenced component ID for placeholder nodes.
	RefID string
	// Children are the resolved child nodes (empty for leaves).
	Children []*Node
	// Scope is the template element this node was instantiated for (nil in
	// root scope).
	Scope any
	// Index is the 0-based template iteration index (nil outside templates).
	Index *int
}

// Materialize builds the display tree from the component with ID "root",
// resolving child references through the adjacency map and expanding
// {componentId, path} list templates. Unresolved IDs become placeholder
// nodes; cycles are cut with placeholder nodes rather than erroring.
func (s *Surface) Materialize() (*Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	root, ok := s.Components["root"]
	if !ok {
		return nil, ErrNoRoot
	}
	budget := &nodeBudget{}
	if err := budget.take(); err != nil {
		return nil, err
	}
	ctx := EvalContext{Root: s.DataModel}
	visiting := map[string]bool{}
	node, err := s.buildNode(root, ctx.Scope, nil, ctx, 0, visiting, budget)
	if err != nil {
		return nil, err
	}
	return node, nil
}

// buildNode materializes one component. visiting holds the component IDs on
// the current path (a cycle is cut, not an error; shared subtrees via DAG
// references remain legal).
func (s *Surface) buildNode(comp *Component, scope any, index *int, rootCtx EvalContext, depth int, visiting map[string]bool, budget *nodeBudget) (*Node, error) {
	if depth > MaxTreeDepth {
		node := &Node{Placeholder: true, RefID: comp.ID}
		return node, nil
	}
	if visiting[comp.ID] {
		// Cycle: render a placeholder instead of recursing forever.
		return &Node{Placeholder: true, RefID: comp.ID}, nil
	}
	visiting[comp.ID] = true
	defer delete(visiting, comp.ID)

	node := &Node{Component: comp, Scope: scope, Index: index}
	childCtx := rootCtx
	childCtx.Scope = scope
	childCtx.Index = index

	var childList *ChildList
	childIDs := []string(nil)
	var singleChild *string
	switch props := comp.Props.(type) {
	case *RowProps:
		childList = &props.Children
	case *ColumnProps:
		childList = &props.Children
	case *ListProps:
		childList = &props.Children
	case *CardProps:
		singleChild = &props.Child
	case *ModalProps:
		// Modal renders trigger then content.
		for _, ref := range []string{props.Trigger, props.Content} {
			child, err := s.buildRef(ref, scope, index, childCtx, depth, visiting, budget)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, child)
		}
		return node, nil
	case *ButtonProps:
		singleChild = &props.Child
	case *TabsProps:
		for i := range props.Tabs {
			tabRef := props.Tabs[i].Child
			child, err := s.buildRef(tabRef, scope, index, childCtx, depth, visiting, budget)
			if err != nil {
				return nil, err
			}
			node.Children = append(node.Children, child)
		}
		return node, nil
	}

	switch {
	case childList != nil && childList.IsTemplate():
		kids, err := s.expandTemplate(childList, scope, index, childCtx, depth, visiting, budget)
		if err != nil {
			return nil, err
		}
		node.Children = kids
	case childList != nil:
		childIDs = childList.IDs
	case singleChild != nil:
		if *singleChild != "" {
			childIDs = []string{*singleChild}
		}
	}
	for _, ref := range childIDs {
		child, err := s.buildRef(ref, scope, index, childCtx, depth, visiting, budget)
		if err != nil {
			return nil, err
		}
		node.Children = append(node.Children, child)
	}
	return node, nil
}

// buildRef resolves one component-ID reference, producing a placeholder for
// unknown IDs.
func (s *Surface) buildRef(ref string, scope any, index *int, ctx EvalContext, depth int, visiting map[string]bool, budget *nodeBudget) (*Node, error) {
	if ref == "" {
		return &Node{Placeholder: true, RefID: ""}, nil
	}
	comp, ok := s.Components[ref]
	if !ok {
		return &Node{Placeholder: true, RefID: ref}, nil
	}
	if err := budget.take(); err != nil {
		return nil, err
	}
	return s.buildNode(comp, scope, index, ctx, depth+1, visiting, budget)
}

// expandTemplate resolves a {componentId, path} ChildList template: the path
// (absolute or scope-relative) must yield an array; the referenced component
// is instantiated once per element with Scope=element and Index=i. Paths
// inside the instantiated subtree resolve against the element first, then
// the root model (absolute paths always hit the root).
func (s *Surface) expandTemplate(cl *ChildList, scope any, index *int, ctx EvalContext, depth int, visiting map[string]bool, budget *nodeBudget) ([]*Node, error) {
	resolveCtx := ctx
	resolveCtx.Scope = scope
	list, ok := resolveTemplateList(resolveCtx, cl.TemplatePath)
	if !ok {
		// Missing or non-array data: render a placeholder (progressive).
		return []*Node{{Placeholder: true, RefID: cl.TemplateID}}, nil
	}
	comp, ok := s.Components[cl.TemplateID]
	if !ok {
		return []*Node{{Placeholder: true, RefID: cl.TemplateID}}, nil
	}
	if len(list) > MaxTemplateItems {
		list = list[:MaxTemplateItems]
	}
	out := make([]*Node, 0, len(list))
	for i := range list {
		idx := i
		if err := budget.take(); err != nil {
			return nil, err
		}
		child, err := s.buildNode(comp, list[i], &idx, ctx, depth+1, visiting, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, child)
	}
	return out, nil
}

// resolveTemplateList resolves a template path to a []any, tolerating
// paths stored as JSON-pointer strings against root or scope.
func resolveTemplateList(ctx EvalContext, path string) ([]any, bool) {
	v, ok := ctx.Resolve(path)
	if !ok {
		return nil, false
	}
	list, ok := v.([]any)
	return list, ok
}

// Walk visits the materialized tree in depth-first order until visit
// returns false. It is the safe iteration helper for renderers.
func (n *Node) Walk(visit func(*Node) bool) {
	if n == nil || !visit(n) {
		return
	}
	for _, c := range n.Children {
		c.Walk(visit)
	}
}

// EvalContext returns the evaluation context for dynamic values in this
// node, given the surface root context.
func (n *Node) EvalContext(root EvalContext) EvalContext {
	root.Scope = n.Scope
	root.Index = n.Index
	return root
}

// jsonptrDepthOK guards against data models deeper than the pointer engine
// supports; used when applying updateDataModel.
func jsonptrDepthOK(v any, depth int) bool {
	if depth > jsonptr.MaxDepth {
		return false
	}
	switch node := v.(type) {
	case map[string]any:
		for _, child := range node {
			if !jsonptrDepthOK(child, depth+1) {
				return false
			}
		}
	case []any:
		for _, child := range node {
			if !jsonptrDepthOK(child, depth+1) {
				return false
			}
		}
	}
	return true
}
