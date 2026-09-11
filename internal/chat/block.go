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
	"strings"

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
	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleUser     = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleAgentTag = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
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
type UserBlock struct{ Text string }

// NewUserBlock sanitizes text into a UserBlock.
func NewUserBlock(text string) *UserBlock { return &UserBlock{Text: SanitizeText(text)} }

func (b *UserBlock) ID() string { return "user" }
func (b *UserBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	prefix := styleUser.Render("┃ you: ")
	body := wrapPlain(b.Text, max(20, width-lipgloss.Width(prefix)))
	return prefix + strings.ReplaceAll(body, "\n", "\n"+strings.Repeat(" ", lipgloss.Width(prefix)))
}

// AgentTextBlock renders agent-authored markdown.
type AgentTextBlock struct{ Text string }

// NewAgentTextBlock sanitizes text into an AgentTextBlock.
func NewAgentTextBlock(text string) *AgentTextBlock { return &AgentTextBlock{Text: SanitizeText(text)} }

func (b *AgentTextBlock) ID() string { return "agent-text" }
func (b *AgentTextBlock) Render(width int) string {
	if width <= 0 {
		width = 80
	}
	out := md.Render(b.Text, width, md.Plain())
	// Glamour's block styles already margin the content; re-clip the
	// width defensively so nothing overflows the pane.
	return out
}

// StatusBlock is a dim informational line.
type StatusBlock struct{ Text string }

// NewStatusBlock sanitizes into a StatusBlock.
func NewStatusBlock(text string) *StatusBlock { return &StatusBlock{Text: SanitizeLine(text)} }

func (b *StatusBlock) ID() string { return "status" }
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
	default:
		styled = styleDim.Render(dot + " " + StateLabel(b.State))
	}
	line := styled + styleDim.Render(" · task "+b.TaskID)
	if b.StatusText != "" {
		line += styleDim.Render(" · " + truncateRunes(b.StatusText, 80))
	}
	return clipLine(line, width)
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
		label = "artifact " + b.ArtifactID
	}
	head := styleAgentTag.Render("▣ "+label) +
		styleDim.Render("  "+b.ArtifactID+" · "+fmt.Sprintf("%dB", b.Bytes))
	if b.Append {
		head += styleDim.Render(" · appended")
	}
	var out []string
	out = append(out, clipLine(head, width))
	if b.Preview != "" {
		out = append(out, clipLines(indent(b.Preview, "  "), width))
	}
	if b.Truncated {
		out = append(out, styleDim.Render("  …"))
	}
	out = append(out, styleDim.Render("  [s] save — coming in a later milestone"))
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
	out := clipLine(styleError.Render("✗ "+b.Friendly), width)
	if raw := strings.TrimSpace(b.Raw); raw != "" {
		peek := raw
		if len(peek) > 2048 {
			peek = peek[:2048] + "…"
		}
		peek = strings.ReplaceAll(SanitizeText(peek), "\n", " ")
		out += "\n" + styleDim.Render(clipLine("  raw: "+truncateRunes(peek, width-4), width))
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
