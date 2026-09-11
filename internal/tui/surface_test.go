package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/jsonptr"
	"github.com/HenryNebula/a2a-tui/internal/agent"
)

// formEnvelopeJSON is a compact contact form (name + gated submit button)
// riding in one createSurface envelope.
const formEnvelopeJSON = `{"version":"v1.0","createSurface":{
	"surfaceId":"tui-form","sendDataModel":true,
	"components":[
		{"id":"root","component":"Column","children":["title","name-field","submit-btn"]},
		{"id":"title","component":"Text","text":"Contact form","variant":"h3"},
		{"id":"name-field","component":"TextField","label":"Name","value":{"path":"/name"},
		 "checks":[{"condition":{"call":"required","args":{"value":{"path":"/name"}}},"message":"Name is required"}]},
		{"id":"submit-btn","component":"Button","child":"submit-btn-label",
		 "checks":[{"condition":{"call":"required","args":{"value":{"path":"/name"}}},"message":"Name is required"}],
		 "action":{"event":{"name":"submit"}}},
		{"id":"submit-btn-label","component":"Text","text":"Submit"}
	],
	"dataModel":{"name":""}}}`

// deliverSurface injects an agent message carrying raw A2UI envelopes the
// way a real agent reply arrives.
func deliverSurface(t *testing.T, a *App, envelopes string) {
	t.Helper()
	part := a2a.NewDataPart(json.RawMessage(envelopes))
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	msg := a2a.NewMessage(a2a.MessageRoleAgent, part)
	msg.ContextID = "ctx-1"
	update(t, a, agentEventMsg{ev: agent.AgentMessageEvent{Msg: msg}})
}

func TestSurfacePaneOpenEditSendEsc(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)
	deliverSurface(t, a, formEnvelopeJSON)

	if a.pane != paneTranscript {
		t.Fatal("pane should still be transcript before focusing")
	}

	// ctrl+f opens the most recent surface from anywhere.
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlF})
	if a.pane != paneSurface {
		t.Fatalf("pane = %v, want surface", a.pane)
	}
	if a.surfacePane.SurfaceID() != "tui-form" {
		t.Fatalf("surface id = %q", a.surfacePane.SurfaceID())
	}

	// The focused surface pane owns the keyboard: typing edits the widget
	// (two-way binding), it must not reach the chat input.
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Grace")})
	if a.input.Value() != "" {
		t.Fatalf("chat input received surface keys: %q", a.input.Value())
	}
	surf := a.session.Engine().Surface("tui-form")
	if v, _ := jsonptr.Get(surf.DataModel, "/name"); v != "Grace" {
		t.Fatalf("/name = %#v, want Grace", v)
	}

	// Tab to the submit button and activate it: the pane builds the action
	// message and the session sends it (capabilities metadata included).
	update(t, a, tea.KeyMsg{Type: tea.KeyTab})
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})

	deadline := time.After(3 * time.Second)
	for conn.getSends() == 0 {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatal("action never sent")
		}
	}
	req := conn.lastSend()
	if req.Message.Role != a2a.MessageRoleUser || len(req.Message.Parts) != 1 {
		t.Fatalf("bad action message: %+v", req.Message)
	}
	if mt, _ := req.Message.Parts[0].Metadata["mimeType"].(string); mt != a2ui.MimeTypeA2UI {
		t.Fatalf("part mimeType = %q", mt)
	}
	var rm a2ui.RendererMessage
	raw, _ := json.Marshal(req.Message.Parts[0].Data())
	if err := json.Unmarshal(raw, &rm); err != nil || rm.Action == nil || rm.Action.Name != "submit" {
		t.Fatalf("action payload = %s (%v)", raw, err)
	}
	if rm.Action.SurfaceID != "tui-form" || rm.Action.SourceComponentID != "submit-btn" {
		t.Fatalf("action = %+v", rm.Action)
	}
	// The action continues the taskless a2ui context.
	if req.Message.ContextID != "ctx-1" {
		t.Fatalf("context id = %q, want ctx-1", req.Message.ContextID)
	}
	// Session metadata: renderer capabilities + the syncing data model.
	if req.Message.Metadata["a2uiRendererCapabilities"] == nil {
		t.Fatalf("capabilities metadata missing: %v", req.Message.Metadata)
	}
	if req.Message.Metadata["a2uiRendererDataModel"] == nil {
		t.Fatalf("data model metadata missing (sendDataModel surface): %v", req.Message.Metadata)
	}

	// The status line reports the sent action.
	if !strings.Contains(rendered(a), "a2ui action submit") {
		t.Fatalf("action-sent status missing:\n%s", rendered(a))
	}

	// esc returns to the transcript pane.
	update(t, a, tea.KeyMsg{Type: tea.KeyEscape})
	if a.pane != paneTranscript {
		t.Fatalf("esc did not return to transcript (pane=%v)", a.pane)
	}
}

func TestSurfacePaneInvalidButtonDoesNotSend(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)
	deliverSurface(t, a, formEnvelopeJSON)

	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlF})
	// /name is empty: the button's required check fails, enter must not
	// produce an outbound message.
	update(t, a, tea.KeyMsg{Type: tea.KeyTab})
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if n := conn.getSends(); n != 0 {
		t.Fatalf("failing-checks button sent %d messages", n)
	}
	if !strings.Contains(a.surfacePane.View(80, 20), "(disabled)") {
		t.Fatalf("disabled button not marked:\n%s", a.surfacePane.View(80, 20))
	}
}

func TestSurfacePaneSyncsOnAgentUpdates(t *testing.T) {
	conn := &fakeConn{}
	a := newTestApp(t, conn)
	deliverSurface(t, a, formEnvelopeJSON)
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlF})
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Ada")})

	// The agent answers the (unsent here) action with an updateComponents
	// replacing the button — the pane must refresh the widget.
	deliverSurface(t, a, `{"version":"v1.0","updateComponents":{"surfaceId":"tui-form",
		"components":[{"id":"submit-btn","component":"Text","text":"Submitted ✓"}]}}`)

	view := a.surfacePane.View(80, 30)
	if !strings.Contains(view, "Submitted ✓") {
		t.Fatalf("agent update not reflected in the pane:\n%s", view)
	}
	// The unsent user edit survives the sync (nothing external overwrote
	// /name).
	if !strings.Contains(view, "Ada") {
		t.Fatalf("user edit lost on sync:\n%s", view)
	}
}

func TestSurfaceCommandAndNoSurfaces(t *testing.T) {
	a := newTestApp(t, nil)

	// No surfaces yet: ctrl+f explains instead of switching panes.
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlF})
	if a.pane != paneTranscript {
		t.Fatal("ctrl+f without surfaces should not open the pane")
	}
	if !strings.Contains(rendered(a), "no live a2ui surfaces") {
		t.Fatalf("hint missing:\n%s", rendered(a))
	}

	// /surface <id> opens the requested surface.
	deliverSurface(t, a, formEnvelopeJSON)
	a.input.SetValue("/surface tui-form")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.pane != paneSurface || a.surfacePane.SurfaceID() != "tui-form" {
		t.Fatalf("pane=%v surface=%q", a.pane, a.surfacePane.SurfaceID())
	}

	// Unknown id reports instead of switching.
	a.pane = paneTranscript
	a.input.SetValue("/surface nope")
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.pane != paneTranscript {
		t.Fatal("unknown surface id switched panes anyway")
	}
	if !strings.Contains(rendered(a), "no live surface") {
		t.Fatalf("error hint missing:\n%s", rendered(a))
	}
}

func TestSurfacePaneOpenURLGating(t *testing.T) {
	a := newTestApp(t, nil)
	deliverSurface(t, a, formEnvelopeJSON)
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlF})
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Ada")})

	// Swap the button for a local openUrl call.
	deliverSurface(t, a, `{"version":"v1.0","updateComponents":{"surfaceId":"tui-form",
		"components":[{"id":"submit-btn","component":"Button","child":"submit-btn-label",
			"action":{"functionCall":{"call":"openUrl","args":{"url":"https://example.com/"}}}},
			{"id":"submit-btn-label","component":"Text","text":"Open docs"}]}}`)

	update(t, a, tea.KeyMsg{Type: tea.KeyTab})
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if !a.surfacePane.PromptActive() {
		view := a.surfacePane.View(80, 20)
		t.Fatalf("openUrl not gated:\n%s", view)
	}
	if !strings.Contains(a.surfacePane.View(80, 20), "open: https://example.com/ (y/n)") {
		t.Fatalf("prompt missing:\n%s", a.surfacePane.View(80, 20))
	}

	// y confirms — but nothing executes; the URL is echoed for copying.
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if a.surfacePane.PromptActive() {
		t.Fatal("prompt survived confirmation")
	}
	if !strings.Contains(a.surfacePane.View(80, 20), "opening is disabled by default") {
		t.Fatalf("refusal missing:\n%s", a.surfacePane.View(80, 20))
	}
}

// A non-http(s) openUrl (javascript:, file:, …) is a validation error: it
// must surface as an error status, never reach the y/n prompt.
func TestSurfacePaneOpenURLBadSchemeIsRefusedNotPrompted(t *testing.T) {
	a := newTestApp(t, nil)
	deliverSurface(t, a, formEnvelopeJSON)
	update(t, a, tea.KeyMsg{Type: tea.KeyCtrlF})
	update(t, a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Ada")})

	// Swap the button for a local openUrl call with a refused scheme.
	deliverSurface(t, a, `{"version":"v1.0","updateComponents":{"surfaceId":"tui-form",
		"components":[{"id":"submit-btn","component":"Button","child":"submit-btn-label",
			"action":{"functionCall":{"call":"openUrl","args":{"url":"javascript:alert(1)"}}}},
			{"id":"submit-btn-label","component":"Text","text":"Open docs"}]}}`)

	update(t, a, tea.KeyMsg{Type: tea.KeyTab})
	update(t, a, tea.KeyMsg{Type: tea.KeyEnter})
	if a.surfacePane.PromptActive() {
		t.Fatal("non-http(s) URL reached the y/n prompt")
	}
	view := a.surfacePane.View(80, 20)
	if !strings.Contains(view, "refused non-http(s)") {
		t.Fatalf("validation error not surfaced:\n%s", view)
	}
}
