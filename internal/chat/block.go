// Package chat models the conversation transcript as a sequence of
// Blocks. Everything an agent sends is sanitized here before it reaches
// the terminal: ANSI-stripped, control-character-cleaned, and
// size-capped. Blocks are immutable once appended; live content (A2UI
// surface snapshots, task-state pills) replaces prior blocks by logical
// ID so the transcript stays compact.
package chat

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/render"
	"github.com/HenryNebula/a2a-tui/internal/md"
)

// Sanitization and sizing limits.
const (
	// MaxBlockBytes caps the sanitized text of any block (64KB).
	MaxBlockBytes = 64 << 10
	// MaxArtifactPreviewLines caps artifact previews.
	MaxArtifactPreviewLines = 10
	// maxDataPreviewChars caps inline data-part previews.
	maxDataPreviewChars = 200
)

var (
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleUser     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleAgentTag = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	styleAgent    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("176"))
	styleError    = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleOK       = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleWarn     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)

// Block is one renderable transcript element.
type Block interface {
	// ID is the block's logical identity: blocks that replace prior
	// content (surface snapshots, live pills) share one ID; the
	// transcript assigns unique instance IDs on append.
	ID() string
	// Render draws the block within width columns.
	Render(width int) string
}

// ---------------------------------------------------------------------------
// Sanitization
// ---------------------------------------------------------------------------

// SanitizeText cleans multi-line agent text: ANSI-stripped, control
// characters removed (newlines preserved), \r\n normalized, capped at
// MaxBlockBytes.
func SanitizeText(s string) string {
	s = ansi.Strip(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	var b strings.Builder
	b.Grow(min(len(s), MaxBlockBytes))
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("  ")
		case r < 0x20 || r == 0x7f:
			// drop other C0 controls and DEL
		case r >= 0x80 && r <= 0x9f:
			// drop C1 controls
		default:
			b.WriteRune(r)
		}
		if b.Len() >= MaxBlockBytes {
			break
		}
	}
	out := b.String()
	// Trim trailing bytes that may have cut a rune.
	return strings.ToValidUTF8(out, "")
}

// SanitizeLine flattens text to a single sanitized line (for headers,
// names, titles).
func SanitizeLine(s string) string {
	s = SanitizeText(s)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes cuts s to at most limit runes, marking truncation.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

// ShortID abbreviates a task/artifact/surface ID for the transcript and
// status line: the last 8 runes of the dashed form. A2A task IDs are
// UUIDv7 — the time prefix is shared by every task in a session, so the
// tail carries the distinguishing entropy. Full IDs stay in the tasks
// dashboard, wire pane and detail views, where they can be copied.
func ShortID(id string) string {
	id = SanitizeLine(id)
	id = strings.ReplaceAll(id, "-", "")
	runes := []rune(id)
	if len(runes) <= 8 {
		return id
	}
	return string(runes[len(runes)-8:])
}

// shortDur renders a duration for a task pill ("1.2s", "4m03s").
func shortDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < 10*time.Second {
		return d.Round(100 * time.Millisecond).String()
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	m := int(d.Minutes())
	return strconv.Itoa(m) + "m" + fmt.Sprintf("%02d", int(d.Seconds())%60) + "s"
}

// indent prefixes every non-empty line.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = prefix + l
		}
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Concrete blocks
// ---------------------------------------------------------------------------

// UserBlock echoes a user-authored chat turn.
type UserBlock struct {
	Text string
	// ReplyTo marks the task this turn answers: one-word answers need
	// their thread context in scrollback.
	ReplyTo string
	// At stamps the turn; zero omits the timestamp.
	At time.Time
}

// NewUserBlock sanitizes text into a UserBlock stamped now.
func NewUserBlock(text string) *UserBlock {
	return &UserBlock{Text: SanitizeText(text), At: time.Now()}
}

func (b *UserBlock) ID() string { return "user" }
func (b *UserBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	prefix := styleUser.Render("┃ you ") + styleDim.Render(stampLabel(b.At))
	tag := ""
	if b.ReplyTo != "" {
		tag = styleDim.Render(" ↳ #" + ShortID(b.ReplyTo) + " ")
	}
	body := wrapPlain(b.Text, max(20, width-lipgloss.Width(prefix)-lipgloss.Width(tag)))
	continuation := strings.Repeat(" ", lipgloss.Width(prefix)+lipgloss.Width(tag))
	return prefix + tag + strings.ReplaceAll(body, "\n", "\n"+continuation)
}

// stamp renders the HH:MM part of t ("" for the zero time).
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format("15:04")
}

// stampLabel renders "[15:04] " ("" for the zero time): metadata in
// brackets, visually demoted from the speaker name.
func stampLabel(t time.Time) string {
	s := stamp(t)
	if s == "" {
		return ""
	}
	return "[" + s + "] "
}

// AgentTextBlock renders agent-authored markdown.
type AgentTextBlock struct {
	Text string
	// Name labels the reply's author (defaults to "agent"; truncated).
	Name string
	// At stamps the reply; zero omits the timestamp.
	At time.Time
}

// NewAgentTextBlock sanitizes text into an AgentTextBlock stamped now.
func NewAgentTextBlock(text string) *AgentTextBlock {
	return &AgentTextBlock{Text: SanitizeText(text), Name: "agent", At: time.Now()}
}

func (b *AgentTextBlock) ID() string { return "agent-text" }
func (b *AgentTextBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	name := truncateRunes(firstNonEmpty(b.Name, "agent"), max(24, width/4))
	prefixStr := "┃ " + name + " " + stampLabel(b.At)
	prefixW := lipgloss.Width(prefixStr)
	// Wrap the markdown to the columns left of the prefix so labeled
	// lines stay within width (same rule as UserBlock).
	out := md.Render(b.Text, max(20, width-prefixW), md.Plain())
	out = strings.Trim(out, "\n")
	prefix := styleAgent.Render("┃ "+name+" ") + styleDim.Render(stampLabel(b.At))
	pad := strings.Repeat(" ", lipgloss.Width(prefix))
	lines := strings.Split(out, "\n")
	first := true
	for i, l := range lines {
		if strings.TrimSpace(ansi.Strip(l)) == "" {
			continue
		}
		if first {
			// Glamour margins every block 2 columns; the label already
			// separates, so drop that margin on line one — ANSI-aware,
			// since glamour may open the line with escape codes.
			l = trimLeftSpacesANSI(l, 2)
			// Re-clip in case a wide element (table/code) still
			// overflowed the reduced budget.
			l = clipLine(l, max(20, width-prefixW))
			first = false
		}
		lines[i] = prefix + l
		prefix = pad // only the first content line is labeled
	}
	return strings.Join(lines, "\n")
}

// reLeadingEscape matches one CSI escape sequence at the start of a line.
var reLeadingEscape = regexp.MustCompile(`^\x1b\[[0-9;?]*[A-Za-z]`)

// trimLeftSpacesANSI removes up to n leading plain spaces, skipping ANSI
// escape sequences (which never count as width).
func trimLeftSpacesANSI(l string, n int) string {
	for n > 0 {
		if m := reLeadingEscape.FindString(l); m != "" {
			l = l[len(m):]
			continue
		}
		if strings.HasPrefix(l, " ") {
			l = l[1:]
			n--
			continue
		}
		break
	}
	return l
}

// StatusBlock is a dim informational line.
type StatusBlock struct {
	Text string
	id   string
}

// NewStatusBlock sanitizes into a StatusBlock.
func NewStatusBlock(text string) *StatusBlock { return &StatusBlock{Text: SanitizeLine(text)} }

// NewStatusBlockID sanitizes into a StatusBlock with a fixed logical ID
// (so it can be replaced or removed later by that ID).
func NewStatusBlockID(id, text string) *StatusBlock {
	return &StatusBlock{Text: SanitizeLine(text), id: id}
}

func (b *StatusBlock) ID() string {
	if b.id != "" {
		return b.id
	}
	return "status"
}

// firstNonEmpty returns the first non-empty argument.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
func (b *StatusBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	return styleDim.Render(clipLine("  "+b.Text, width))
}

// TaskStateBlock renders a task state pill line. Terminal states are
// emphasized.
type TaskStateBlock struct {
	TaskID     string
	State      a2a.TaskState
	StatusText string
	// At is when the task was first observed (drives the duration shown
	// on terminal pills); zero omits it.
	At time.Time
	// Elapsed, when non-zero, is shown on terminal pills ("took 1.2s").
	Elapsed time.Duration
}

// NewTaskStateBlock sanitizes into a TaskStateBlock.
func NewTaskStateBlock(taskID string, state a2a.TaskState, statusText string) *TaskStateBlock {
	return &TaskStateBlock{TaskID: SanitizeLine(taskID), State: state, StatusText: SanitizeLine(statusText)}
}

func (b *TaskStateBlock) ID() string { return "task-state:" + b.TaskID }

// StateLabel renders a friendly state name.
func StateLabel(s a2a.TaskState) string {
	name := strings.TrimPrefix(string(s), "TASK_STATE_")
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

func (b *TaskStateBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	dot := "●"
	var styled string
	switch b.State {
	case a2a.TaskStateCompleted:
		styled = styleOK.Bold(true).Render(dot + " " + StateLabel(b.State))
	case a2a.TaskStateFailed, a2a.TaskStateRejected, a2a.TaskStateCanceled:
		styled = styleError.Bold(true).Render(dot + " " + StateLabel(b.State))
	case a2a.TaskStateInputRequired, a2a.TaskStateAuthRequired:
		styled = styleWarn.Bold(true).Render(dot + " " + StateLabel(b.State))
	case a2a.TaskStateWorking, a2a.TaskStateSubmitted:
		// Live work must not read as dimmed/done.
		styled = styleWarn.Bold(true).Render(dot + " " + StateLabel(b.State))
	default:
		styled = styleDim.Render(dot + " " + StateLabel(b.State))
	}
	line := styled + styleDim.Render(" · #"+ShortID(b.TaskID))
	if b.State.Terminal() && b.Elapsed >= 100*time.Millisecond {
		line += styleDim.Render(" · took " + shortDur(b.Elapsed))
	}
	// Pills stay quiet — StatusText is deliberately not rendered: every
	// state update also delivers its text as an agent message block,
	// which this used to repeat verbatim ("sleeping 4s" twice in a row).
	// Push-delivered updates are the exception: the ⇄ badge marks the
	// delivery origin, which exists nowhere else in the transcript.
	if strings.HasPrefix(b.StatusText, "⇄ push") {
		line += styleDim.Render(" · " + truncateRunes(b.StatusText, 80))
	}
	return clipLine("  "+line, width)
}

// ArtifactBlock previews one artifact: header plus a capped preview of
// its first text/data part.
type ArtifactBlock struct {
	TaskID      string
	ArtifactID  string
	Name        string
	Description string
	Preview     string
	Bytes       int
	Truncated   bool
	Append      bool
}

// NewArtifactBlock builds a preview block from an artifact update.
func NewArtifactBlock(taskID string, a *a2a.Artifact) *ArtifactBlock {
	if a == nil {
		return nil
	}
	b := &ArtifactBlock{
		TaskID:      SanitizeLine(taskID),
		ArtifactID:  SanitizeLine(string(a.ID)),
		Name:        SanitizeLine(a.Name),
		Description: SanitizeLine(a.Description),
	}
	var preview string
	for _, p := range a.Parts {
		if p == nil {
			continue
		}
		switch {
		case p.Text() != "":
			preview = p.Text()
			b.Bytes += len(p.Text())
		case p.Raw() != nil:
			b.Bytes += len(p.Raw())
		case p.Data() != nil:
			if enc, err := json.Marshal(p.Data()); err == nil {
				b.Bytes += len(enc)
				if preview == "" {
					preview = string(enc)
				}
			}
		case p.URL() != "":
			if preview == "" {
				preview = string(p.URL())
			}
			b.Bytes += len(p.URL())
		}
		if preview != "" {
			break
		}
	}
	lines := strings.Split(SanitizeText(preview), "\n")
	if len(lines) > MaxArtifactPreviewLines {
		lines = lines[:MaxArtifactPreviewLines]
		b.Truncated = true
	}
	b.Preview = strings.TrimRight(strings.Join(lines, "\n"), "\n ")
	return b
}

func (b *ArtifactBlock) ID() string { return "artifact:" + b.TaskID + ":" + b.ArtifactID }

func (b *ArtifactBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	label := b.Name
	if label == "" {
		label = "artifact"
	}
	head := styleAgentTag.Render("▣ "+truncateRunes(label, 40)) +
		styleDim.Render("  #"+ShortID(b.ArtifactID)+" · "+fmt.Sprintf("%dB", b.Bytes))
	if b.Append {
		head += styleDim.Render(" · appended")
	}
	var out []string
	out = append(out, clipLine("  "+head, width))
	if b.Preview != "" {
		out = append(out, clipLines(indent(md.Render(b.Preview, max(20, width-2), md.Plain()), "  "), width))
	}
	if b.Truncated {
		out = append(out, styleDim.Render("  … preview truncated"))
	}
	return strings.Join(out, "\n")
}

// ErrorBlock renders a friendly A2A error plus an optional raw-JSON peek.
type ErrorBlock struct {
	Friendly string
	Raw      string
}

// NewErrorBlock sanitizes inputs; raw is the wire peek (already small).
func NewErrorBlock(friendly, raw string) *ErrorBlock {
	return &ErrorBlock{Friendly: SanitizeLine(friendly), Raw: strings.ToValidUTF8(raw, "")}
}

func (b *ErrorBlock) ID() string { return "error" }
func (b *ErrorBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	out := clipLine("  "+styleError.Bold(true).Render("✗ "+b.Friendly), width)
	if raw := strings.TrimSpace(b.Raw); raw != "" {
		peek := raw
		if len(peek) > 2048 {
			peek = peek[:2048] + "…"
		}
		peek = strings.ReplaceAll(SanitizeText(peek), "\n", " ")
		out += "\n" + styleDim.Render(clipLine("  raw: "+truncateRunes(peek, width-4), width))
		out += "\n" + styleDim.Render(clipLine("  inspect the exchange: ctrl+w", width))
	}
	return out
}

// SurfaceSnapshotBlock renders the current static view of an A2UI
// surface. Repeated snapshots for the same surface replace each other in
// the transcript.
type SurfaceSnapshotBlock struct {
	SurfaceID string
	surf      *a2ui.Surface
	deleted   bool
}

// NewSurfaceSnapshotBlock captures the engine's current view of a
// surface (nil when deleted).
func NewSurfaceSnapshotBlock(surfaceID string, surf *a2ui.Surface) *SurfaceSnapshotBlock {
	return &SurfaceSnapshotBlock{SurfaceID: SanitizeLine(surfaceID), surf: surf}
}

// NewSurfaceDeletedBlock marks a surface as deleted.
func NewSurfaceDeletedBlock(surfaceID string) *SurfaceSnapshotBlock {
	return &SurfaceSnapshotBlock{SurfaceID: SanitizeLine(surfaceID), deleted: true}
}

func (b *SurfaceSnapshotBlock) ID() string { return "surface:" + b.SurfaceID }

func (b *SurfaceSnapshotBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	head := styleDim.Render("◇ a2ui surface " + b.SurfaceID + " · [ctrl+f focus]")
	if b.deleted || b.surf == nil {
		return clipLine(head+styleDim.Render(" (deleted)"), width)
	}
	body := render.Render(b.surf, width)
	if strings.TrimSpace(body) == "" {
		return clipLine(head+styleDim.Render(" (empty)"), width)
	}
	return head + "\n" + body
}

// DataPreviewBlock renders a non-A2UI structured data part as one
// compact line.
type DataPreviewBlock struct {
	MediaType string
	Preview   string
}

// NewDataPreviewBlock renders a data part inline.
func NewDataPreviewBlock(mediaType string, value any) *DataPreviewBlock {
	enc, err := json.Marshal(value)
	if err != nil {
		return &DataPreviewBlock{MediaType: mediaType, Preview: "(unencodable data)"}
	}
	return &DataPreviewBlock{
		MediaType: SanitizeLine(mediaType),
		Preview:   truncateRunes(SanitizeLine(string(enc)), maxDataPreviewChars),
	}
}

func (b *DataPreviewBlock) ID() string { return "data" }
func (b *DataPreviewBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	mt := b.MediaType
	if mt == "" {
		mt = "json"
	}
	return styleDim.Render(clipLine("· data "+mt+": "+b.Preview, width))
}

// FilePartBlock renders a file/url part reference (never fetched).
type FilePartBlock struct {
	Label string // filename or "file"/"url"
	URL   string
	Bytes int
}

// NewFilePartBlock builds a file/url reference line from a part.
func NewFilePartBlock(p *a2a.Part) *FilePartBlock {
	if p == nil {
		return nil
	}
	label := SanitizeLine(p.Filename)
	if label == "" {
		label = "file"
	}
	b := &FilePartBlock{Label: label, Bytes: len(p.Raw())}
	if u := p.URL(); u != "" {
		b.URL = SanitizeLine(string(u))
		if p.Filename == "" {
			b.Label = "url"
		}
	}
	return b
}

func (b *FilePartBlock) ID() string { return "file" }
func (b *FilePartBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	line := "· " + b.Label
	if b.Bytes > 0 {
		line += fmt.Sprintf(" · %dB", b.Bytes)
	}
	if b.URL != "" {
		line += " · " + b.URL
	}
	return styleDim.Render(clipLine(line, width))
}

// ---------------------------------------------------------------------------
// Width helpers
// ---------------------------------------------------------------------------

// wrapPlain hard-wraps plain text at width display columns.
func wrapPlain(s string, width int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		out = append(out, ansi.Hardwrap(line, width, true))
	}
	return strings.Join(out, "\n")
}

// clipLine truncates a (possibly styled) line to width display columns.
func clipLine(s string, width int) string {
	if lipgloss.Width(s) <= width {
		return s
	}
	return ansi.Truncate(s, width, "…")
}

// clipLines applies clipLine to every line.
func clipLines(s string, width int) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = clipLine(l, width)
	}
	return strings.Join(lines, "\n")
}
