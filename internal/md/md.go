// Package md renders markdown to terminal-styled text via glamour.
//
// glamour term renderers are neither concurrent-safe nor cheap to build,
// so this package serializes rendering behind a mutex and caches both
// renderers (per wrap width) and rendered output. Callers on terminals
// with an Ascii color profile should pass plain=true, which skips
// glamour entirely and returns the (sanitized upstream) input.
package md

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Limits.
const (
	// MaxInputBytes caps rendered markdown input (64KB).
	MaxInputBytes = 64 << 10
	// maxRenderers caps cached per-width renderers.
	maxRenderers = 16
	// maxCache caps rendered-output cache entries; the oldest entry is
	// evicted FIFO beyond that.
	maxCache = 100
)

type cacheKey struct {
	text  string
	width int
	plain bool
}

var (
	mu        sync.Mutex
	renderers = map[int]*glamour.TermRenderer{}
	rOrder    []int
	cache     = map[cacheKey]string{}
	cOrder    []cacheKey
)

// Plain reports whether the terminal color profile is Ascii (no styles).
func Plain() bool {
	return lipgloss.ColorProfile() == termenv.Ascii
}

// Render renders markdown at the given wrap width. On plain terminals,
// render errors, or oversized input it falls back to the input text.
func Render(text string, width int, plain bool) string {
	if text == "" {
		return ""
	}
	if len(text) > MaxInputBytes {
		text = text[:MaxInputBytes]
	}
	if plain || width <= 0 {
		return text
	}
	// Hostile input mitigation: a single unbreakable token longer than
	// the width makes glamour's wrapper quadratic. Hard-break such runs
	// before rendering (display only — the content survives intact).
	text = hardBreakLongTokens(text, width)

	key := cacheKey{text: text, width: width, plain: plain}
	mu.Lock()
	defer mu.Unlock()
	if out, ok := cache[key]; ok {
		return out
	}

	r, err := rendererFor(width)
	if err != nil {
		return remember(key, text)
	}
	out, err := r.Render(text)
	if err != nil || out == "" {
		return remember(key, text)
	}
	return remember(key, out)
}

// hardBreakLongTokens inserts newlines inside any run of non-space runes
// longer than width*2, so glamour never sees an unbreakable token wider
// than 2× the wrap width.
func hardBreakLongTokens(text string, width int) string {
	limit := width * 2
	if limit <= 0 || len(text) <= limit {
		return text
	}
	var b strings.Builder
	b.Grow(len(text) + len(text)/limit)
	run := 0
	for _, r := range text {
		b.WriteRune(r)
		if r == '\n' || r == ' ' || r == '\t' {
			run = 0
			continue
		}
		run += 1
		if run == limit {
			b.WriteByte('\n')
			run = 0
		}
	}
	return b.String()
}

// rendererFor returns a cached renderer wrapping at width. Callers must
// hold mu.
func rendererFor(width int) (*glamour.TermRenderer, error) {
	if r, ok := renderers[width]; ok {
		return r, nil
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil, err
	}
	if len(rOrder) >= maxRenderers {
		delete(renderers, rOrder[0])
		rOrder = rOrder[1:]
	}
	renderers[width] = r
	rOrder = append(rOrder, width)
	return r, nil
}

// remember stores out under key, evicting FIFO beyond maxCache. Callers
// must hold mu.
func remember(key cacheKey, out string) string {
	if _, ok := cache[key]; !ok {
		if len(cOrder) >= maxCache {
			delete(cache, cOrder[0])
			cOrder = cOrder[1:]
		}
		cOrder = append(cOrder, key)
	}
	cache[key] = out
	return out
}
