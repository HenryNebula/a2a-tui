package chat

import (
	"context"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

// Splitter turns A2A messages (and artifacts, task snapshots) into
// transcript blocks. All agent-supplied strings are sanitized inside the
// block constructors; nothing unsanitized reaches Render.
//
// A2UI data parts (part metadata mimeType application/a2ui+json) are fed
// through the session's engine here — application is serialized on the
// UI thread that processes session events, so envelope order matches
// wire order.

// A2UIApplier is the subset of *a2ui.Engine the splitter needs. It lets
// tests (and future widget layers) substitute a fake engine.
type A2UIApplier interface {
	Apply(ctx context.Context, envelopes []a2ui.Envelope) []a2ui.ApplyError
	Surface(id string) *a2ui.Surface
}

// SplitMessage converts one message into blocks. Text parts become
// markdown blocks (agent role) or user echoes; data parts become A2UI
// surface snapshots or compact previews; file/url parts become reference
// lines.
func SplitMessage(msg *a2a.Message, eng A2UIApplier) []Block {
	if msg == nil {
		return nil
	}
	var blocks []Block

	appendText := func(text string) {
		if text == "" {
			return
		}
		if msg.Role == a2a.MessageRoleUser {
			blocks = append(blocks, NewUserBlock(text))
		} else {
			blocks = append(blocks, NewAgentTextBlock(text))
		}
	}

	for _, part := range msg.Parts {
		if part == nil {
			continue
		}
		switch part.Content.(type) {
		case a2a.Text:
			appendText(part.Text())
		case a2a.Data:
			if envs, ok := extractA2UI(part, eng); ok {
				blocks = append(blocks, envs...)
				continue
			}
			blocks = append(blocks, NewDataPreviewBlock(part.MediaType, part.Data()))
		case a2a.URL, a2a.Raw:
			blocks = append(blocks, NewFilePartBlock(part))
		default:
			blocks = append(blocks, NewStatusBlock("part with unknown content"))
		}
	}
	return blocks
}

// extractA2UI detects an A2UI data part, applies its envelopes through
// the engine and returns the resulting snapshot blocks. The bool result
// reports whether the part was A2UI at all.
func extractA2UI(part *a2a.Part, eng A2UIApplier) ([]Block, bool) {
	envs, ok, derr := a2ui.ExtractEnvelopes(part.Data(), part.Metadata)
	if !ok {
		return nil, false
	}
	if derr != nil {
		return []Block{NewErrorBlock("a2ui: payload did not decode: "+derr.Error(), "")}, true
	}
	if len(envs) == 0 {
		return nil, true
	}
	if eng == nil {
		return []Block{NewErrorBlock("a2ui: no engine available", "")}, true
	}

	// Surface IDs touched by this batch, in envelope order, and the IDs
	// whose last envelope deletes them (rendered as tombstones).
	var ids []string
	deleted := map[string]bool{}
	seen := map[string]bool{}
	for _, env := range envs {
		for _, id := range envelopeSurfaceIDs(env) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
			deleted[id] = env.DeleteSurface != nil
		}
	}

	var blocks []Block
	if errs := eng.Apply(context.Background(), envs); len(errs) > 0 {
		for _, ae := range errs {
			blocks = append(blocks, NewErrorBlock("a2ui: "+ae.Error(), ""))
		}
	}
	for _, id := range ids {
		if surf := eng.Surface(id); surf != nil {
			blocks = append(blocks, NewSurfaceSnapshotBlock(id, surf))
		} else if deleted[id] {
			// A deleted surface replaces its snapshot with a tombstone.
			blocks = append(blocks, NewSurfaceDeletedBlock(id))
		}
		// A surface that failed to create/update is represented by the
		// apply-error blocks above; no empty snapshot noise.
	}
	return blocks, true
}

// envelopeSurfaceIDs lists the surface IDs an envelope addresses.
func envelopeSurfaceIDs(env a2ui.Envelope) []string {
	var ids []string
	switch {
	case env.CreateSurface != nil:
		ids = append(ids, env.CreateSurface.SurfaceID)
	case env.UpdateComponents != nil:
		ids = append(ids, env.UpdateComponents.SurfaceID)
	case env.UpdateDataModel != nil:
		ids = append(ids, env.UpdateDataModel.SurfaceID)
	case env.DeleteSurface != nil:
		ids = append(ids, env.DeleteSurface.SurfaceID)
	}
	return ids
}

// BlocksForTask renders a task snapshot for /task and /history: state
// pill, artifacts, then history messages.
func BlocksForTask(t *a2a.Task, eng A2UIApplier) []Block {
	if t == nil {
		return nil
	}
	var blocks []Block
	status := statusText(t)
	blocks = append(blocks, NewTaskStateBlock(string(t.ID), t.Status.State, status))
	for _, a := range t.Artifacts {
		if b := NewArtifactBlock(string(t.ID), a); b != nil {
			blocks = append(blocks, b)
		}
	}
	for _, m := range t.History {
		blocks = append(blocks, SplitMessage(m, eng)...)
	}
	// A terminal task's final reply often lives only in the status
	// message (the pill omits it there); render it as an agent turn
	// unless the history already carries the same text.
	if t.Status.State.Terminal() && status != "" && !historyHasText(t, status) {
		blocks = append(blocks, NewAgentTextBlock(status))
	}
	return blocks
}

// historyHasText reports whether any history message carries text
// identical to s.
func historyHasText(t *a2a.Task, s string) bool {
	for _, m := range t.History {
		if m == nil {
			continue
		}
		for _, p := range m.Parts {
			if p != nil && p.Text() == s {
				return true
			}
		}
	}
	return false
}

// statusText returns the task's status-message text, one part per line:
// the final reply is usually markdown, and flattening newlines would
// destroy its structure on the fallback rendering path.
func statusText(t *a2a.Task) string {
	if t == nil || t.Status.Message == nil {
		return ""
	}
	var lines []string
	for _, p := range t.Status.Message.Parts {
		if p != nil && p.Text() != "" {
			lines = append(lines, p.Text())
		}
	}
	return SanitizeText(strings.Join(lines, "\n"))
}
