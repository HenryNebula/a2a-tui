package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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

// maxWireBodyBytes caps each pretty-printed body; maxRawBodyBytes caps
// the raw bytes fed to the indenter (bound on work for huge captures).
const (
	maxWireBodyBytes = 4 << 10 // 4KB
	maxRawBodyBytes  = 64 << 10
)

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

// Update consumes the pane's keys: scroll bindings. It reports whether the
// key was consumed; anything else (including typing) falls through to the
// input box. Clearing is bound to ctrl+l at the app level (WireClear),
// which calls Clear directly.
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
	hint := "ctrl+l clear · ↑/↓ pgup/pgdn scroll · frames: " + strconv.Itoa(len(p.visible()))
	if last := p.lastStatus(); last != "" {
		hint += " · last " + last
	}
	if n := strings.Count(p.cache, "\n") + 1; n > bodyHeight {
		hint += " · " + strconv.Itoa(int(p.viewport.ScrollPercent())) + "%"
	}
	footer := styleDim.Render(cell(hint, width))
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
	sep := func(idx int) string {
		label := fmt.Sprintf(" %d/%d ", idx+1, len(entries))
		n := max(0, min(width, 60)-lipgloss.Width(label))
		return styleDim.Render(strings.Repeat("·", n/2)) + styleDim.Render(label) + styleDim.Render(strings.Repeat("·", n-n/2))
	}
	parts := make([]string, 0, len(frames)*2)
	for i, f := range frames {
		parts = append(parts, f, sep(i))
	}
	return strings.Join(parts[:len(parts)-1], "\n")
}

// lastStatus renders the newest exchange's response summary for the
// footer ("← 200") so the verdict is visible regardless of scroll.
func (p *WirePane) lastStatus() string {
	entries := p.visible()
	if len(entries) == 0 {
		return ""
	}
	e := entries[len(entries)-1]
	if e.Err != "" {
		return "✗ " + chat.ShortID(chat.SanitizeLine(e.Err))
	}
	if e.Status == 0 {
		return ""
	}
	return "← " + strconv.Itoa(e.Status)
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
		statusStyle := styleCardOK.Bold(true)
		if e.Status >= 400 || e.Status == 0 {
			statusStyle = styleError.Bold(true)
		}
		b.WriteString("\n" + statusStyle.Render(cell(respHead, width)) + "\n")
		if body := prettyBody(e.RespBody); body != "" {
			b.WriteString(indentBody(body, width))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// prettyBody pretty-prints a JSON body, capping the OUTPUT at line
// boundaries (indenting first and then cutting keeps the pretty form;
// cutting the raw bytes first usually breaks the JSON). SSE bodies (a
// whole event stream in one capture) are split into their events so each
// data payload gets its own indentation instead of one clipped line per
// event.
func prettyBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	capped := body
	capRaw := false
	if len(capped) > maxRawBodyBytes {
		capped = capped[:maxRawBodyBytes]
		capRaw = true
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, capped, "", "  "); err != nil {
		// Not parseable JSON (SSE or truncated stream): show the raw
		// text, pretty-printed per SSE event where applicable.
		buf.Reset()
		buf.Write(capped)
	}
	out := chat.SanitizeText(buf.String())
	if out != "" && looksLikeSSE(out) {
		out = prettySSE(out)
	}
	out = strings.TrimRight(out, "\n \r")
	if len(out) > maxWireBodyBytes {
		out = truncateAtLine(out, maxWireBodyBytes) + "\n… (truncated)"
	} else if capRaw {
		out += "\n… (truncated)"
	}
	return out
}

// truncateAtLine cuts s to at most limit bytes without splitting a line.
func truncateAtLine(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := s[:limit]
	if i := strings.LastIndex(cut, "\n"); i > 0 {
		return cut[:i]
	}
	return cut
}

// looksLikeSSE reports whether the body is an event stream: at least one
// `data:` field line.
func looksLikeSSE(s string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(l, "data:") {
			return true
		}
	}
	return false
}

// prettySSE renders each event as a block: the id/event fields dim and
// compact, the data payload pretty-printed JSON. Events are separated by
// a blank line.
func prettySSE(s string) string {
	events := strings.Split(s, "\n\n")
	out := make([]string, 0, len(events))
	for _, ev := range events {
		var fields, datas []string
		for _, l := range strings.Split(ev, "\n") {
			if payload, ok := strings.CutPrefix(l, "data:"); ok {
				var buf bytes.Buffer
				if json.Indent(&buf, []byte(strings.TrimSpace(payload)), "  ", "  ") == nil {
					datas = append(datas, "data:"+buf.String())
					continue
				}
			}
			if strings.TrimSpace(l) != "" {
				fields = append(fields, l)
			}
		}
		block := strings.Join(fields, " ")
		if len(datas) > 0 {
			if block != "" {
				block += "\n"
			}
			block += strings.Join(datas, "\n")
		}
		if block != "" {
			out = append(out, block)
		}
	}
	return strings.Join(out, "\n\n")
}

// reJSONKey matches an indented JSON object key at the start of a line.
var reJSONKey = regexp.MustCompile(`^(\s*)"([^"]+)":`)

// indentBody indents a body under the frame header, highlighting JSON
// keys and clipping each line with an ellipsis marker.
func indentBody(body string, width int) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if m := reJSONKey.FindStringSubmatch(l); m != nil {
			l = m[1] + styleCardLabel.Render(`"`+m[2]+`":`) + l[len(m[0]):]
		}
		lines[i] = styleCardValue.Render(clipMark("  "+l, width))
	}
	return strings.Join(lines, "\n") + "\n"
}

// clipMark clips s to width display cells, marking a cut with an
// ellipsis.
func clipMark(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return cell(s[:max(0, len(s)-2)], width-1) + "…"
}

// byteLabel renders a body size with a truncation marker.
func byteLabel(n int, trunc bool) string {
	s := fmt.Sprintf("%dB", n)
	if trunc {
		s += "+"
	}
	return s
}
