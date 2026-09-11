package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/chat"
	"github.com/HenryNebula/a2a-tui/internal/wirelog"
)

// WirePane renders the wire log: one framed block per captured HTTP
// exchange (method + url + timestamp + latency, pretty-printed request
// body, response status + content-type + body), newest at the bottom with
// transcript-style auto-follow. Capture itself lives in internal/wirelog
// (the shared transport); this pane only displays snapshots.
type WirePane struct {
	viewport viewport.Model

	entries   []wirelog.Entry
	clearedAt time.Time // entries started before this are hidden (c key)

	// render cache: pretty-printing hundreds of frames per keystroke is
	// wasteful, so the document is rebuilt only when the log moves.
	cache    string
	cacheKey wireCacheKey
}

// wireCacheKey identifies a renderable snapshot state.
type wireCacheKey struct {
	n        int
	lastAt   time.Time
	cleared  time.Time
	width    int
	lastByte int // cheap content fingerprint of the last body
}

// maxWireBodyBytes caps each pretty-printed body before indentation.
const maxWireBodyBytes = 4 << 10 // 4KB

// NewWirePane returns an empty wire pane.
func NewWirePane() WirePane {
	return WirePane{viewport: viewport.New(0, 0)}
}

// openWirePane switches to the wire pane (ctrl+w, /wire). Frames already
// captured stay visible even when capture is off.
func (a *App) openWirePane() {
	a.pane = paneWire
}

// Sync installs a fresh wire-log snapshot (oldest first).
func (p *WirePane) Sync(entries []wirelog.Entry) {
	p.entries = entries
}

// Clear hides everything captured so far (the ring itself is untouched;
// capture continues and new frames reappear).
func (p *WirePane) Clear() {
	p.clearedAt = time.Now()
}

// Update consumes the pane's keys: scroll bindings plus "c" to clear. It
// reports whether the key was consumed; anything else falls through to the
// input box.
func (p *WirePane) Update(msg tea.Msg) bool {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return false
	}
	switch k.String() {
	case "up":
		p.viewport.ScrollUp(1)
	case "down":
		p.viewport.ScrollDown(1)
	case "pgup":
		p.viewport.HalfPageUp()
	case "pgdown":
		p.viewport.HalfPageDown()
	case "home":
		p.viewport.GotoTop()
	case "end":
		p.viewport.GotoBottom()
	case "c":
		p.Clear()
	default:
		return false
	}
	return true
}

// View renders the framed log into the given geometry, staying glued to
// the bottom when the user has not scrolled up.
func (p *WirePane) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	footer := styleDim.Render(cell("c clear · up/down scroll · frames: "+strconv.Itoa(len(p.visible())), width))
	bodyHeight := max(1, height-1)

	key := p.cacheKeyFor(width)
	if key != p.cacheKey || p.cache == "" {
		p.cacheKey = key
		p.cache = p.render(width)
	}

	atBottom := p.viewport.AtBottom()
	p.viewport.Width = width
	p.viewport.Height = bodyHeight
	p.viewport.SetContent(p.cache)
	if atBottom {
		p.viewport.GotoBottom() // auto-follow like the transcript
	}
	return p.viewport.View() + "\n" + footer
}

// visible returns the entries not hidden by the last clear.
func (p *WirePane) visible() []wirelog.Entry {
	if p.clearedAt.IsZero() {
		return p.entries
	}
	out := p.entries[:0:0]
	for _, e := range p.entries {
		if e.At.After(p.clearedAt) {
			out = append(out, e)
		}
	}
	return out
}

// cacheKeyFor builds the snapshot identity for the render cache.
func (p *WirePane) cacheKeyFor(width int) wireCacheKey {
	key := wireCacheKey{n: len(p.entries), cleared: p.clearedAt, width: width}
	if n := len(p.entries); n > 0 {
		last := p.entries[n-1]
		key.lastAt = last.At
		key.lastByte = len(last.RespBody) + last.Status
	}
	return key
}

// render lays out one framed block per exchange.
func (p *WirePane) render(width int) string {
	entries := p.visible()
	if len(entries) == 0 {
		if p.clearedAt.IsZero() {
			return styleDim.Render("wire log empty — traffic appears here as it is captured")
		}
		return styleDim.Render("wire log cleared — new frames appear here")
	}

	frames := make([]string, 0, len(entries))
	for _, e := range entries {
		frames = append(frames, p.frame(e, width))
	}
	return strings.Join(frames, "\n"+styleDim.Render(strings.Repeat("·", max(8, min(width, 40))))+"\n")
}

// frame renders one exchange: request head + body, response head + body.
func (p *WirePane) frame(e wirelog.Entry, width int) string {
	var b strings.Builder
	head := fmt.Sprintf("→ %s %s · %s · %s",
		e.Method, chat.SanitizeLine(e.URL), e.At.Format("15:04:05"), e.Duration)
	b.WriteString(styleCardLabel.Render(cell(head, width)) + "\n")
	if e.Err != "" {
		b.WriteString(styleError.Render(cell("  ✗ "+chat.SanitizeLine(e.Err), width)) + "\n")
	}
	if body := prettyBody(e.ReqBody); body != "" {
		b.WriteString(indentBody(body, width))
	}

	if e.Status != 0 || len(e.RespBody) > 0 {
		respHead := fmt.Sprintf("← %d %s · %s", e.Status,
			chat.SanitizeLine(e.RespHeaders.Get("Content-Type")), byteLabel(len(e.RespBody), e.RespTrunc))
		b.WriteString(styleCardValue.Render(cell(respHead, width)) + "\n")
		if body := prettyBody(e.RespBody); body != "" {
			b.WriteString(indentBody(body, width))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// prettyBody pretty-prints a JSON body, capping it first: truncated JSON
// often fails to indent, in which case the raw (still capped) text shows.
func prettyBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	truncated := false
	if len(body) > maxWireBodyBytes {
		body = body[:maxWireBodyBytes]
		truncated = true
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		buf.Reset()
		buf.Write(body)
	}
	out := chat.SanitizeText(buf.String())
	if truncated {
		out += "\n… (truncated)"
	}
	return out
}

// indentBody indents a body under the frame header, clipping each line.
func indentBody(body string, width int) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = styleDim.Render(cell("  "+l, width))
	}
	return strings.Join(lines, "\n") + "\n"
}

// byteLabel renders a body size with a truncation marker.
func byteLabel(n int, trunc bool) string {
	s := fmt.Sprintf("%dB", n)
	if trunc {
		s += "+"
	}
	return s
}
