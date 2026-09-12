package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
)

func engine(t *testing.T) *a2ui.Engine {
	t.Helper()
	return a2ui.NewEngine()
}

func agentMessage(parts ...*a2a.Part) *a2a.Message {
	return a2a.NewMessage(a2a.MessageRoleAgent, parts...)
}

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"keeps newlines", "line1\nline2", "line1\nline2"},
		{"normalizes crlf", "a\r\nb\rc", "a\nb\nc"},
		{"strips ansi", "ok\x1b[31mred\x1b[0m", "okred"},
		{"drops control chars", "a\x00\x07b", "ab"},
		{"tabs to spaces", "a\tb", "a  b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeText(tc.in); got != tc.want {
				t.Fatalf("SanitizeText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeTextSizeCap(t *testing.T) {
	big := strings.Repeat("a", MaxBlockBytes+5000)
	got := SanitizeText(big)
	if len(got) > MaxBlockBytes+16 {
		t.Fatalf("cap exceeded: %d", len(got))
	}
	if !strings.HasSuffix(got, "aaa") {
		t.Fatal("unexpected tail")
	}
}

func TestSplitTextParts(t *testing.T) {
	msg := agentMessage(a2a.NewTextPart("# Hello\n"), a2a.NewTextPart("world"))
	blocks := SplitMessage(msg, nil)
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	for _, b := range blocks {
		ab, ok := b.(*AgentTextBlock)
		if !ok {
			t.Fatalf("want AgentTextBlock, got %T", b)
		}
		if strings.Contains(ab.Text, "#") == false && ab.Text == "" {
			t.Fatal("empty text")
		}
	}
}

func TestSplitUserRoleEcho(t *testing.T) {
	msg := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("from history"))
	blocks := SplitMessage(msg, nil)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	if _, ok := blocks[0].(*UserBlock); !ok {
		t.Fatalf("want UserBlock, got %T", blocks[0])
	}
}

func TestSplitA2UIDataPart(t *testing.T) {
	eng := engine(t)
	part := a2a.NewDataPart([]any{map[string]any{
		"version":       a2ui.VersionV1,
		"createSurface": map[string]any{"surfaceId": "s1", "components": []any{}},
	}})
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	msg := agentMessage(a2a.NewTextPart("here is a form"), part)

	blocks := SplitMessage(msg, eng)
	if len(blocks) != 2 {
		t.Fatalf("want text + snapshot blocks, got %d", len(blocks))
	}
	snap, ok := blocks[1].(*SurfaceSnapshotBlock)
	if !ok {
		t.Fatalf("want SurfaceSnapshotBlock, got %T", blocks[1])
	}
	if snap.ID() != "surface:s1" {
		t.Fatalf("id = %q", snap.ID())
	}
	if eng.Surface("s1") == nil {
		t.Fatal("surface not applied to engine")
	}

	// A second envelope updates the surface: the transcript replaces the
	// prior snapshot instead of growing.
	part2 := a2a.NewDataPart([]any{map[string]any{
		"version":         a2ui.VersionV1,
		"updateDataModel": map[string]any{"surfaceId": "s1", "path": "/title", "value": "updated"},
	}})
	part2.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	blocks2 := SplitMessage(agentMessage(part2), eng)
	if len(blocks2) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks2))
	}
	if blocks2[0].ID() != "surface:s1" {
		t.Fatalf("replacement id = %q", blocks2[0].ID())
	}
}

func TestSplitA2UIDeleteSurface(t *testing.T) {
	eng := engine(t)
	mk := func(env map[string]any) *a2a.Part {
		p := a2a.NewDataPart([]any{env})
		p.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
		return p
	}
	SplitMessage(agentMessage(mk(map[string]any{
		"version":       a2ui.VersionV1,
		"createSurface": map[string]any{"surfaceId": "sx", "components": []any{}},
	})), eng)
	blocks := SplitMessage(agentMessage(mk(map[string]any{
		"version":       a2ui.VersionV1,
		"deleteSurface": map[string]any{"surfaceId": "sx"},
	})), eng)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	snap, ok := blocks[0].(*SurfaceSnapshotBlock)
	if !ok || !snap.deleted {
		t.Fatalf("want deleted snapshot, got %#v", blocks[0])
	}
}

func TestSplitA2UIBadEnvelope(t *testing.T) {
	eng := engine(t)
	// updateComponents for a surface that was never created: engine
	// reports an apply error.
	part := a2a.NewDataPart([]any{map[string]any{
		"version": a2ui.VersionV1,
		"updateComponents": map[string]any{"surfaceId": "ghost", "components": []any{
			map[string]any{"id": "root", "component": "Text", "props": map[string]any{"value": "hi"}},
		}},
	}})
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	blocks := SplitMessage(agentMessage(part), eng)
	if len(blocks) != 1 {
		t.Fatalf("want 1 error block, got %d", len(blocks))
	}
	if _, ok := blocks[0].(*ErrorBlock); !ok {
		t.Fatalf("want ErrorBlock, got %T", blocks[0])
	}
}

func TestSplitA2UIUndecodablePayload(t *testing.T) {
	eng := engine(t)
	// updateComponents with an empty components list fails envelope
	// decoding; the splitter must surface an error, not silence.
	part := a2a.NewDataPart([]any{map[string]any{
		"version":          a2ui.VersionV1,
		"updateComponents": map[string]any{"surfaceId": "s1", "components": []any{}},
	}})
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	blocks := SplitMessage(agentMessage(part), eng)
	if len(blocks) != 1 {
		t.Fatalf("want 1 error block, got %d", len(blocks))
	}
	if _, ok := blocks[0].(*ErrorBlock); !ok {
		t.Fatalf("want ErrorBlock, got %T", blocks[0])
	}
}

func TestSplitPlainDataPart(t *testing.T) {
	part := a2a.NewDataPart(map[string]any{"k": "v"})
	blocks := SplitMessage(agentMessage(part), nil)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	dp, ok := blocks[0].(*DataPreviewBlock)
	if !ok {
		t.Fatalf("want DataPreviewBlock, got %T", blocks[0])
	}
	if !strings.Contains(dp.Preview, `"k"`) {
		t.Fatalf("preview = %q", dp.Preview)
	}
}

func TestSplitFileURLParts(t *testing.T) {
	raw := a2a.NewRawPart([]byte("0123456789"))
	raw.Filename = "data.bin"
	url := a2a.NewFileURLPart("https://agent.example/file.png", "image/png")
	url.Filename = "file.png"

	blocks := SplitMessage(agentMessage(raw, url), nil)
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	fp, ok := blocks[0].(*FilePartBlock)
	if !ok || fp.Label != "data.bin" || fp.Bytes != 10 {
		t.Fatalf("raw part block = %#v", blocks[0])
	}
	fu, ok := blocks[1].(*FilePartBlock)
	if !ok || fu.URL != "https://agent.example/file.png" {
		t.Fatalf("url part block = %#v", blocks[1])
	}
	rendered := fu.Render(80)
	if !strings.Contains(rendered, "file.png") || !strings.Contains(rendered, "https://agent.example/file.png") {
		t.Fatalf("url render = %q", rendered)
	}
}

func TestSanitizeInBlocks(t *testing.T) {
	msg := agentMessage(a2a.NewTextPart("\x1b[31mred\x1b[0m text"))
	blocks := SplitMessage(msg, nil)
	ab := blocks[0].(*AgentTextBlock)
	if strings.Contains(ab.Text, "\x1b") {
		t.Fatalf("ANSI survived: %q", ab.Text)
	}
	if ab.Text != "red text" {
		t.Fatalf("text = %q", ab.Text)
	}
}

func TestTranscriptReplaceByID(t *testing.T) {
	tr := NewTranscript()
	tr.Append(NewStatusBlock("one"))
	// ReplaceByID with no match appends.
	if tr.ReplaceByID("surface:s1", NewSurfaceSnapshotBlock("s1", nil)) {
		t.Fatal("reported replace on empty transcript")
	}
	if tr.Len() != 2 {
		t.Fatalf("len = %d", tr.Len())
	}
	// Replace with match swaps in place.
	if !tr.ReplaceByID("surface:s1", NewSurfaceDeletedBlock("s1")) {
		t.Fatal("replace reported no match")
	}
	if tr.Len() != 2 {
		t.Fatalf("len after replace = %d", tr.Len())
	}
	out := tr.Render(60, 0)
	if strings.Contains(out, "deleted") == false {
		t.Fatalf("render should show tombstone: %q", out)
	}
	if strings.Count(out, "a2ui surface") != 1 {
		t.Fatalf("snapshot not replaced: %q", out)
	}
}

func TestTranscriptRenderCaches(t *testing.T) {
	tr := NewTranscript()
	tr.Append(NewStatusBlock("cached"))
	tr.Render(50, 0)
	tr.mu.Lock()
	n := len(tr.cache)
	tr.mu.Unlock()
	if n != 1 {
		t.Fatalf("cache = %d, want 1", n)
	}
	tr.Render(50, 0)
	tr.mu.Lock()
	n2 := len(tr.cache)
	tr.mu.Unlock()
	if n2 != 1 {
		t.Fatalf("cache grew: %d", n2)
	}
}

func TestArtifactBlockPreviewCap(t *testing.T) {
	text := strings.Repeat("line\n", 30)
	art := &a2a.Artifact{ID: "a-1", Name: "big.txt", Parts: a2a.ContentParts{a2a.NewTextPart(text)}}
	b := NewArtifactBlock("t-1", art)
	if b.Truncated != true {
		t.Fatal("expected truncation flag")
	}
	if n := strings.Count(b.Preview, "\n"); n != MaxArtifactPreviewLines-1 {
		t.Fatalf("preview lines = %d", n+1)
	}
	out := b.Render(80)
	if !strings.Contains(out, "big.txt") || !strings.Contains(out, "…") {
		t.Fatalf("render = %q", out)
	}
}

func TestTaskStateBlockStyling(t *testing.T) {
	b := NewTaskStateBlock("t-9", a2a.TaskStateInputRequired, "need more info")
	if got := StateLabel(a2a.TaskStateInputRequired); got != "input-required" {
		t.Fatalf("label = %q", got)
	}
	out := b.Render(80)
	if !strings.Contains(out, "input-required") || !strings.Contains(out, "#"+ShortID("t-9")) {
		t.Fatalf("render = %q", out)
	}
	// Interactive pills stay quiet: the question rides the status line.
	if strings.Contains(out, "need more info") {
		t.Fatalf("interactive pill repeats status text: %q", out)
	}
	// Live progress keeps its status text.
	work := NewTaskStateBlock("t-9", a2a.TaskStateWorking, "sleeping")
	if !strings.Contains(work.Render(80), "sleeping") {
		t.Fatalf("working pill lost status text: %q", work.Render(80))
	}
	// Terminal pills drop the status text (the final agent message
	// follows as its own block) and show the elapsed time when known.
	done := NewTaskStateBlock("t-9", a2a.TaskStateCompleted, "all done")
	done.Elapsed = 1500 * time.Millisecond
	outDone := done.Render(80)
	if strings.Contains(outDone, "all done") {
		t.Fatalf("terminal pill repeats status text: %q", outDone)
	}
	if !strings.Contains(outDone, "took 1.5s") {
		t.Fatalf("elapsed missing: %q", outDone)
	}
}

func TestErrorBlockRawPeek(t *testing.T) {
	b := NewErrorBlock("TaskNotFound (-32001) [TASK_NOT_FOUND]: nope", `{"jsonrpc":"2.0","error":{"code":-32001}}`)
	out := b.Render(60)
	if !strings.Contains(out, "TaskNotFound (-32001)") {
		t.Fatalf("friendly missing: %q", out)
	}
	if !strings.Contains(out, "raw:") {
		t.Fatalf("raw peek missing: %q", out)
	}
	// Raw peek is capped.
	long := strings.Repeat("x", 5000)
	b2 := NewErrorBlock("err", long)
	if len(b2.Render(200)) > 300 {
		t.Fatalf("raw peek not clipped: %d", len(b2.Render(200)))
	}
}

func TestRenderWidthClip(t *testing.T) {
	b := NewStatusBlock(strings.Repeat("wide ", 200))
	line := b.Render(30)
	if w := lineVisualWidth(line); w > 30 {
		t.Fatalf("status line width %d > 30", w)
	}
}

func TestBlocksForTask(t *testing.T) {
	eng := engine(t)
	task := &a2a.Task{
		ID:        "t-42",
		ContextID: "c-42",
		Status:    a2a.TaskStatus{State: a2a.TaskStateCompleted},
		Artifacts: []*a2a.Artifact{{ID: "a-1", Parts: a2a.ContentParts{a2a.NewTextPart("artifact body")}}},
		History: []*a2a.Message{
			a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("question")),
			a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("answer")),
		},
	}
	blocks := BlocksForTask(task, eng)
	if len(blocks) != 4 {
		t.Fatalf("want 4 blocks (state, artifact, 2 messages), got %d", len(blocks))
	}
	if _, ok := blocks[0].(*TaskStateBlock); !ok {
		t.Fatalf("first block = %T", blocks[0])
	}
}

// lineVisualWidth strips ANSI and measures display width of one line.
func lineVisualWidth(s string) int {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return len([]rune(s))
}

func TestMoveToEndFreshInstanceID(t *testing.T) {
	// Regression: MoveToEnd once reused the current seq, colliding with
	// the instanceID of a block appended just before — the render cache
	// then rendered the pill as the message (and vice versa).
	tr := NewTranscript()
	tr.Append(NewStatusBlock("one"))
	tr.ReplaceByID("task-state:t1", NewTaskStateBlock("t1", a2a.TaskStateCompleted, ""))
	msg := NewAgentTextBlock("hello")
	tr.Append(msg)
	// Both the moved pill and the message must keep distinct renders.
	tr.MoveToEnd("task-state:t1")
	out := tr.Render(80, 0)
	if !strings.Contains(out, "hello") || !strings.Contains(stripANSI(out), "completed") {
		t.Fatalf("render lost a block:\n%s", out)
	}
	if strings.Count(out, "hello") != 1 {
		t.Fatalf("message duplicated:\n%s", out)
	}
}

func stripANSI(s string) string {
	out := make([]rune, 0, len(s))
	esc := false
	for _, r := range s {
		if r == 0x1b {
			esc = true
			continue
		}
		if esc {
			if r == 'm' {
				esc = false
			}
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
