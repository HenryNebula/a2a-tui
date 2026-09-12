package chat

import (
	"strings"
	"sync"
)

// Transcript limits.
const (
	// maxRenderCache caps per-block render cache entries (FIFO evict).
	maxRenderCache = 256
	// maxTranscriptLines bounds the rendered transcript so a hostile
	// agent cannot grow it without limit (older content falls off the
	// top; the viewport scrolls within what remains).
	maxTranscriptLines = 20000
)

// Transcript is an ordered sequence of blocks with a render cache keyed
// by block instance and width. It is safe for concurrent use, though the
// TUI mutates it only from Update.
type Transcript struct {
	mu      sync.Mutex
	entries []entry
	cache   map[cacheKey]string
	order   []cacheKey
	seq     int
}

type entry struct {
	instanceID int
	logical    string // block.ID() — may repeat ("", for one-shots)
	block      Block
}

type cacheKey struct {
	instanceID int
	width      int
}

// NewTranscript returns an empty transcript.
func NewTranscript() *Transcript {
	return &Transcript{cache: map[cacheKey]string{}}
}

// Append adds blocks to the end of the transcript.
func (t *Transcript) Append(blocks ...Block) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range blocks {
		if b == nil {
			continue
		}
		t.seq++
		t.entries = append(t.entries, entry{instanceID: t.seq, logical: b.ID(), block: b})
	}
}

// ReplaceByID replaces every block whose logical ID matches id with b.
// When nothing matches, b is appended (a snapshot for a surface the
// transcript has not seen yet). It reports whether an existing block was
// replaced.
func (t *Transcript) ReplaceByID(id string, b Block) bool {
	if b == nil || id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	replaced := false
	for i := range t.entries {
		if t.entries[i].logical == id {
			if replaced {
				// Collapse duplicate logical blocks: drop extras so a
				// replace-by-id keeps exactly one live block.
				t.entries = append(t.entries[:i], t.entries[i+1:]...)
				continue
			}
			t.entries[i] = entry{instanceID: t.seq, logical: id, block: b}
			replaced = true
		}
	}
	if !replaced {
		t.entries = append(t.entries, entry{instanceID: t.seq, logical: id, block: b})
	}
	return replaced
}

// Len returns the number of blocks.
func (t *Transcript) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// RemoveByID drops every block with the given logical ID (e.g. stale
// onboarding hints once they no longer apply).
func (t *Transcript) RemoveByID(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	kept := t.entries[:0]
	for _, e := range t.entries {
		if e.logical != id {
			kept = append(kept, e)
		}
	}
	t.entries = kept
	t.seq++
}

// MoveToEnd relocates the (first) block with the given logical ID to the
// end of the transcript. Task pills use this to land after the turn's
// content: the state event usually arrives before the final message or
// artifact, and a "completed" marker above the reply reads backwards.
func (t *Transcript) MoveToEnd(id string) {
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, e := range t.entries {
		if e.logical == id {
			t.entries = append(t.entries[:i], t.entries[i+1:]...)
			// Fresh, unique instance ID: instanceIDs key the render
			// cache, so a reused one would render this block as
			// whatever last took that ID (e.g. the message appended
			// just before the move).
			t.seq++
			e.instanceID = t.seq
			t.entries = append(t.entries, e)
			return
		}
	}
}

// Render draws all blocks joined by blank-line separators, no wider than
// width. height bounds the output line budget (older lines fall off).
func (t *Transcript) Render(width, height int) string {
	if width <= 0 {
		width = 80
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var parts []string
	for _, e := range t.entries {
		key := cacheKey{instanceID: e.instanceID, width: width}
		out, ok := t.cache[key]
		if !ok {
			out = e.block.Render(width)
			t.rememberLocked(key, out)
		}
		parts = append(parts, out)
	}
	// Blank line between blocks: turns read as turns, not a packed log.
	joined := strings.Join(parts, "\n\n")

	lines := strings.Split(joined, "\n")
	if maxLines := maxTranscriptLines; len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	if height > 0 && len(lines) > height*8 {
		// Hard ceiling relative to the viewport so pathological content
		// cannot accumulate unbounded within one session.
		lines = lines[len(lines)-height*8:]
	}
	return strings.Join(lines, "\n")
}

// rememberLocked caches a render result (callers hold t.mu).
func (t *Transcript) rememberLocked(key cacheKey, out string) {
	if _, ok := t.cache[key]; !ok {
		if len(t.order) >= maxRenderCache {
			delete(t.cache, t.order[0])
			t.order = t.order[1:]
		}
		t.order = append(t.order, key)
	}
	t.cache[key] = out
}
