package agent

import (
	"sort"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
)

// maxTitleRunes caps the derived title of a task.
const maxTitleRunes = 80

// TaskMeta is the registry's projection of one observed task. It is fed
// from every event that flows through a Session and powers the task
// dashboards introduced in later milestones.
type TaskMeta struct {
	// ID is the task ID.
	ID string
	// ContextID groups related tasks.
	ContextID string
	// State is the last observed task state.
	State a2a.TaskState
	// Title is a short derived title (first message or status text).
	Title string
	// Artifacts counts observed artifact creations (not appends).
	Artifacts int
	// CreatedAt is when the task was first observed locally.
	CreatedAt time.Time
	// UpdatedAt is when the task was last touched by any event.
	UpdatedAt time.Time
}

// Terminal reports whether State is a terminal task state.
func (m TaskMeta) Terminal() bool { return m.State.Terminal() }

// Registry tracks observed tasks. It is safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	tasks map[string]*TaskMeta
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{tasks: map[string]*TaskMeta{}}
}

// Observe folds an event into the registry. Nil events are ignored.
func (r *Registry) Observe(ev a2a.Event) {
	if ev == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	switch e := ev.(type) {
	case *a2a.Task:
		if e == nil || e.ID == "" {
			return
		}
		m := r.entry(string(e.ID), now)
		m.ContextID = e.ContextID
		if st := e.Status.State; st != "" {
			m.State = st
		}
		if title := taskTitle(e); title != "" {
			rememberTitle(m, title)
		}
		if n := len(e.Artifacts); n > m.Artifacts {
			m.Artifacts = n
		}
		m.UpdatedAt = now
	case *a2a.TaskStatusUpdateEvent:
		if e == nil || e.TaskID == "" {
			return
		}
		m := r.entry(string(e.TaskID), now)
		m.ContextID = e.ContextID
		if st := e.Status.State; st != "" {
			m.State = st
		}
		if e.Status.Message != nil {
			if title := messageText(e.Status.Message); title != "" {
				rememberTitle(m, title)
			}
		}
		m.UpdatedAt = now
	case *a2a.TaskArtifactUpdateEvent:
		if e == nil || e.TaskID == "" {
			return
		}
		m := r.entry(string(e.TaskID), now)
		m.ContextID = e.ContextID
		if !e.Append {
			m.Artifacts++
		}
		m.UpdatedAt = now
	case *a2a.Message:
		if e == nil || e.TaskID == "" {
			return
		}
		m := r.entry(string(e.TaskID), now)
		if m.ContextID == "" {
			m.ContextID = e.ContextID
		}
		if title := messageText(e); title != "" {
			rememberTitle(m, title)
		}
		m.UpdatedAt = now
	}
}

// Get returns a copy of the task meta, if observed.
func (r *Registry) Get(id string) (TaskMeta, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.tasks[id]
	if !ok {
		return TaskMeta{}, false
	}
	return *m, true
}

// List returns all observed tasks, most recently updated first.
func (r *Registry) List() []TaskMeta {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]TaskMeta, 0, len(r.tasks))
	for _, m := range r.tasks {
		out = append(out, *m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

// Count returns the number of observed tasks.
func (r *Registry) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tasks)
}

// entry returns (creating if needed) the registry slot for id. The
// caller must hold r.mu; the returned meta is only valid while the lock
// stays held.
func (r *Registry) entry(id string, now time.Time) *TaskMeta {
	if m, ok := r.tasks[id]; ok {
		return m
	}
	m := &TaskMeta{ID: id, CreatedAt: now, UpdatedAt: now}
	r.tasks[id] = m
	return m
}

// taskTitle derives a title from a task snapshot: the first history
// message, else the status message.
func taskTitle(t *a2a.Task) string {
	if len(t.History) > 0 {
		if s := messageText(t.History[0]); s != "" {
			return s
		}
	}
	if t.Status.Message != nil {
		return messageText(t.Status.Message)
	}
	return ""
}

// messageText joins the text parts of a message (sanitized, capped).
func messageText(m *a2a.Message) string {
	if m == nil {
		return ""
	}
	var out string
	for _, p := range m.Parts {
		if p == nil {
			continue
		}
		if t := p.Text(); t != "" {
			if out != "" {
				out += " "
			}
			out += t
		}
	}
	return sanitizeRunes(out, maxTitleRunes)
}

// rememberTitle keeps the first non-empty title observed for a task.
func rememberTitle(m *TaskMeta, title string) {
	if m.Title == "" && title != "" {
		m.Title = title
	}
}

// sanitizeRunes flattens to one sanitized line and caps at limit runes.
func sanitizeRunes(s string, limit int) string {
	s = sanitize(s)
	runes := []rune(s)
	if len(runes) > limit {
		runes = runes[:limit-1]
		return string(runes) + "…"
	}
	return s
}
