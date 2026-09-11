package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/widget"
	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
)

// SurfacePane is the interactive A2UI surface pane: a widget.Model over
// the engine's live surface, a scrolling viewport for tall surfaces, and a
// footer with the surface id, data-model dirty state and key hints. Fired
// actions leave the pane through the send callback the App installs; local
// {functionCall} actions run through the engine's registry here, with
// openUrl gated behind an explicit y/n prompt (nothing is ever opened).
type SurfacePane struct {
	// eng is the session's A2UI engine (surface source of truth).
	eng *a2ui.Engine
	// send delivers a fired action to the agent (installed by App).
	send func(widget.ActionOut) tea.Cmd

	widget    *widget.Model
	surfaceID string
	syncedAt  time.Time
	viewport  viewport.Model

	// status is a transient footer message ("action sent…").
	status string
	// prompt is the active openUrl confirmation, if any.
	prompt *openPrompt
}

// openPrompt is the gated openUrl confirmation state.
type openPrompt struct{ url string }

// NewSurfacePane returns an unopened surface pane.
func NewSurfacePane() SurfacePane {
	return SurfacePane{viewport: viewport.New(0, 0)}
}

// Open binds the pane to a live engine surface and builds the widget.
func (p *SurfacePane) Open(eng *a2ui.Engine, surfaceID string, send func(widget.ActionOut) tea.Cmd) error {
	surf := eng.Surface(surfaceID)
	if surf == nil {
		return fmt.Errorf("no live surface %q", surfaceID)
	}
	w, err := widget.New(surf)
	if err != nil {
		return err
	}
	p.eng = eng
	p.send = send
	p.widget = w
	p.surfaceID = surfaceID
	p.syncedAt = surf.UpdatedAt
	p.status = ""
	p.prompt = nil
	p.viewport.GotoTop()
	return nil
}

// Close drops the pane's widget state (used by /disconnect).
func (p *SurfacePane) Close() {
	p.widget = nil
	p.eng = nil
	p.surfaceID = ""
	p.status = ""
	p.prompt = nil
}

// Active reports whether the pane holds a live widget.
func (p *SurfacePane) Active() bool { return p.widget != nil }

// SurfaceID returns the open surface's ID ("" when closed).
func (p *SurfacePane) SurfaceID() string { return p.surfaceID }

// PromptActive reports whether an openUrl confirmation is pending (esc
// then cancels the prompt instead of leaving the pane).
func (p *SurfacePane) PromptActive() bool { return p.prompt != nil }

// ---------------------------------------------------------------------------
// App-side surface glue
// ---------------------------------------------------------------------------

// cmdSurface implements /surface <id> (no argument: the most recently
// updated live surface).
func (a *App) cmdSurface(args []string) {
	id := ""
	if len(args) > 0 {
		id = args[0]
	}
	a.openSurface(id)
}

// openSurface focuses the interactive pane on one live surface.
func (a *App) openSurface(id string) {
	if a.session == nil {
		a.addStatus("not connected — /connect <url-or-name> first")
		a.refreshTranscript()
		return
	}
	eng := a.session.Engine()
	if id == "" {
		id = a.mostRecentSurface(eng)
	}
	if id == "" {
		a.addStatus("no live a2ui surfaces (try asking the agent for one)")
		a.refreshTranscript()
		return
	}
	if err := a.surfacePane.Open(eng, id, a.sendSurfaceAction); err != nil {
		a.addStatus("/surface: " + err.Error())
		a.refreshTranscript()
		return
	}
	a.pane = paneSurface
}

// mostRecentSurface returns the live surface ID with the latest update.
func (a *App) mostRecentSurface(eng *a2ui.Engine) string {
	var best string
	var bestAt time.Time
	for _, id := range eng.SurfaceIDs() {
		if surf := eng.Surface(id); surf != nil && (best == "" || surf.UpdatedAt.After(bestAt)) {
			best, bestAt = id, surf.UpdatedAt
		}
	}
	return best
}

// sendSurfaceAction translates a fired widget action into an outbound A2A
// message (data part + A2UI metadata merged by the session) and sends it
// through the current chat path.
func (a *App) sendSurfaceAction(out widget.ActionOut) tea.Cmd {
	if a.session == nil {
		a.addError("surface action", errors.New("not connected"))
		a.refreshTranscript()
		return nil
	}
	msg, err := actionMessage(out, a.lastContextID)
	if err != nil {
		a.addError("surface action", err)
		a.refreshTranscript()
		return nil
	}
	opts := agent.SendOptions{}
	a.inflight++
	if a.streamMode {
		a.session.SendRawStreaming(context.Background(), msg, opts)
	} else {
		a.session.SendRaw(context.Background(), msg, opts)
	}
	a.addStatus("a2ui action " + out.Action.Name + " → " + a.session.Conn().BaseURL())
	a.refreshTranscript()
	return a.spinnerCmd()
}

// syncSurfacePane refreshes the focused widget after the engine applied
// envelope batches (surface updates arrive as ordinary agent messages).
func (a *App) syncSurfacePane() {
	if a.session == nil || !a.surfacePane.Active() {
		return
	}
	a.surfacePane.Sync(a.session.Engine())
}

// Sync refreshes the widget after the engine applied envelopes: a
// surface's UpdatedAt moves on every engine mutation. A deleted surface
// closes the pane content.
func (p *SurfacePane) Sync(eng *a2ui.Engine) {
	if p.widget == nil || eng == nil {
		return
	}
	surf := eng.Surface(p.surfaceID)
	if surf == nil {
		p.widget = nil
		p.status = "surface " + p.surfaceID + " closed by the agent"
		return
	}
	if !surf.UpdatedAt.Equal(p.syncedAt) {
		p.syncedAt = surf.UpdatedAt
		if err := p.widget.Refresh(); err != nil {
			p.status = "surface refresh: " + err.Error()
		}
	}
}

// Update routes one key: the openUrl prompt first, then the widget, then
// the viewport scroll keys. It returns the accumulated command (action
// sends re-arm the spinner) and whether the key was consumed.
func (p *SurfacePane) Update(msg tea.Msg) (tea.Cmd, bool) {
	key, ok := msg.(tea.KeyMsg)
	if !ok || p.widget == nil {
		return nil, false
	}
	s := key.String()

	// The gated openUrl confirmation swallows everything; y confirms
	// (still refusing to execute — the URL is only echoed for copying),
	// anything else cancels.
	if p.prompt != nil {
		switch s {
		case "y", "Y":
			p.status = "opening is disabled by default — copy the URL: " + p.prompt.url
		case "n", "N", "esc":
			p.status = "open canceled"
		default:
			p.status = "open? y/n"
		}
		p.prompt = nil
		return nil, true
	}

	switch s {
	case "[":
		p.switchSurface(-1)
		return nil, true
	case "]":
		p.switchSurface(1)
		return nil, true
	}

	out, consumed, err := p.widget.Update(msg)
	if err != nil {
		p.status = "edit: " + err.Error()
	}
	var cmd tea.Cmd
	if out.Action != nil || out.LocalFunctionCall != nil {
		cmd = p.dispatch(out)
	}
	if consumed {
		return cmd, true
	}
	switch s {
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
		return nil, false
	}
	return nil, true
}

// dispatch delivers a widget action: agent events go through the send
// callback; local function calls execute through the engine's registry
// (openUrl gates behind the y/n prompt).
func (p *SurfacePane) dispatch(out widget.ActionOut) tea.Cmd {
	if out.Action != nil {
		if p.send == nil {
			p.status = "action cannot be sent (no session)"
			return nil
		}
		p.status = "action " + out.Action.Name + " sent…"
		return p.send(out)
	}
	if out.LocalFunctionCall == nil || p.eng == nil {
		return nil
	}
	call := out.LocalFunctionCall
	surf := p.eng.Surface(p.surfaceID)
	if surf == nil {
		return nil
	}
	funcs := p.eng.Functions()
	ctx := surf.EvalContextFor(funcs)
	res, err := a2ui.Dynamic{Kind: a2ui.KindCall, Call: call}.Evaluate(ctx)
	switch {
	case errors.Is(err, a2ui.ErrGated):
		// Only a genuinely gated call (openUrl on an http(s) URL) may reach
		// the y/n prompt; any other error — e.g. openUrl refusing a
		// non-http(s) scheme — is a validation failure and renders as such.
		url := callArgString(call, "url", ctx)
		if url == "" {
			p.status = "openUrl requested without a usable URL — refused"
			return nil
		}
		p.prompt = &openPrompt{url: url}
	case err != nil:
		p.status = call.Name + ": " + err.Error()
	default:
		p.status = call.Name + " → " + a2ui.Stringify(res)
	}
	return nil
}

// callArgString extracts a call's string argument, resolving {"path": …}
// bindings through the surface's evaluation context.
func callArgString(call *a2ui.Call, name string, ctx a2ui.EvalContext) string {
	switch v := call.Args[name].(type) {
	case string:
		return chat.SanitizeLine(v)
	case map[string]any:
		if path, ok := v["path"].(string); ok {
			if val, found := ctx.Resolve(path); found {
				return chat.SanitizeLine(a2ui.Stringify(val))
			}
		}
	}
	return ""
}

// switchSurface cycles to the previous/next live surface when several are
// live.
func (p *SurfacePane) switchSurface(delta int) {
	if p.eng == nil {
		return
	}
	ids := p.eng.SurfaceIDs()
	if len(ids) < 2 {
		return
	}
	cur := 0
	for i, id := range ids {
		if id == p.surfaceID {
			cur = i
			break
		}
	}
	next := ((cur+delta)%len(ids) + len(ids)) % len(ids)
	if err := p.Open(p.eng, ids[next], p.send); err != nil {
		p.status = "switch: " + err.Error()
	}
}

// View renders the pane: the interactive surface in a viewport plus the
// footer (status line over the key-hint line).
func (p *SurfacePane) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if p.widget == nil {
		msg := "no surface focused — ask the agent for one, then /surface <id> or ctrl+f"
		if p.status != "" {
			msg = p.status
		}
		return styleDim.Render(cell(msg, width))
	}
	bodyHeight := height - 2 // status + hint lines
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	p.viewport.Width = width
	p.viewport.Height = bodyHeight
	p.viewport.SetContent(p.widget.View(width, 0))
	return p.viewport.View() + "\n" + p.footer(width)
}

// footer renders the pane's status line and key hints.
func (p *SurfacePane) footer(width int) string {
	if p.prompt != nil {
		return styleSurfacePrompt.Render(cell("open: "+p.prompt.url+" (y/n)", width))
	}
	hint := "surface " + p.surfaceID
	if p.widget.Dirty() {
		hint += " · model edited"
	}
	hint += " · tab cycle · enter activate · esc back"
	if p.eng != nil {
		if ids := p.eng.SurfaceIDs(); len(ids) > 1 {
			hint += " · [ ] switch: " + strings.Join(ids, " ")
		}
	}
	first := styleDim.Render(cell(hint, width))
	if p.status == "" {
		return first
	}
	return first + "\n" + styleStatus.Render(cell(" "+p.status, width))
}

// actionMessage builds the outbound SDK message for a fired widget
// action: one data part carrying the renderer→agent action envelope JSON,
// marked application/a2ui+json, continuing the conversation context.
func actionMessage(out widget.ActionOut, contextID string) (*a2a.Message, error) {
	if out.Action == nil {
		return nil, errors.New("no action payload")
	}
	version := out.EnvelopeVersion
	if version == "" {
		version = a2ui.VersionV1
	}
	raw, err := json.Marshal(a2ui.RendererMessage{Version: version, Action: out.Action})
	if err != nil {
		return nil, err
	}
	part := a2a.NewDataPart(json.RawMessage(raw))
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	msg := a2a.NewMessage(a2a.MessageRoleUser, part)
	if contextID != "" {
		msg.ContextID = contextID
	}
	return msg, nil
}
