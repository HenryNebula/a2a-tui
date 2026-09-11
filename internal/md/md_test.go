package md

import (
	"strings"
	"sync"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	out := Render("# Title\n\nsome *emphasis* and `code`\n", 80, false)
	if out == "" {
		t.Fatal("empty render")
	}
	if !strings.Contains(strings.ToLower(out), "title") {
		t.Fatalf("heading text missing: %q", out)
	}
	if !strings.Contains(out, "emphasis") {
		t.Fatalf("body text missing: %q", out)
	}
}

func TestPlainFallsBack(t *testing.T) {
	in := "# Title\n\nbody text\n"
	if got := Render(in, 80, true); got != in {
		t.Fatalf("plain render = %q, want input", got)
	}
}

func TestWidthWrap(t *testing.T) {
	words := strings.Repeat("word ", 100)
	out := Render(words, 40, false)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		// Glamour may pad lines; ANSI-free length check with a margin
		// for the style's own padding.
		if len(strings.TrimSpace(line)) > 44 {
			t.Fatalf("line not wrapped at width 40: %q", line)
		}
	}
}

func TestCacheHit(t *testing.T) {
	in := "cached content\n"
	first := Render(in, 40, false)
	mu.Lock()
	n := len(cache)
	mu.Unlock()
	second := Render(in, 40, false)
	if first != second {
		t.Fatal("second render differs")
	}
	mu.Lock()
	n2 := len(cache)
	mu.Unlock()
	if n2 != n {
		t.Fatalf("cache grew on repeat render: %d → %d", n, n2)
	}
}

func TestInputCap(t *testing.T) {
	big := strings.Repeat("a", MaxInputBytes+1000)
	out := Render(big, 40, false)
	if len(out) > MaxInputBytes*3 {
		t.Fatalf("output suspiciously large for capped input: %d", len(out))
	}
}

func TestConcurrentRender(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			width := 40
			if i%2 == 1 {
				width = 41
			}
			_ = Render(strings.Repeat("x", i+1)+" markdown **bold**\n", width, false)
		}(i)
	}
	wg.Wait()
}
