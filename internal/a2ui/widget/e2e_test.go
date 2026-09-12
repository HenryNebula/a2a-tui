package widget_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/a2ui"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/jsonptr"
	"github.com/HenryNebula/a2a-tui/internal/a2ui/widget"
	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
	"github.com/HenryNebula/a2a-tui/internal/fixtureagent"
)

// e2eTimeout bounds each poll phase; everything runs in-process against
// the fixture agent, so the margins only guard against scheduler hiccups.
const e2eTimeout = 15 * time.Second

// TestFormActionRoundTrip is the issue-#25 end-to-end: connect to the
// fixture agent, request its contact-form surface, fill it through the
// interactive widget, fire the submit action over the wire, and watch the
// agent's updateDataModel/updateComponents reply mutate the live surface
// ("Submitted ✓" appears in the widget view).
func TestFormActionRoundTrip(t *testing.T) {
	ts, err := fixtureagent.StartTest()
	if err != nil {
		t.Fatalf("fixture agent: %v", err)
	}
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	hc := &http.Client{Timeout: e2eTimeout}
	res, err := agent.Resolve(ctx, hc, ts.URL, agent.Auto)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := agent.NewConn(ctx, res, hc)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	sess := agent.NewSession(conn)
	defer sess.Shutdown()

	// 1. Ask for the form surface (the way the TUI sends a chat turn).
	sess.Send(ctx, "a2ui form", agent.SendOptions{})

	// 2. Drain events, applying agent messages through the same splitter
	//    the TUI uses, until the form surface exists.
	var surfID, contextID string
	applyMessage := func(msg *a2a.Message) {
		if msg == nil {
			return
		}
		if msg.ContextID != "" {
			contextID = msg.ContextID
		}
		chat.SplitMessage(msg, sess.Engine())
		for _, id := range sess.Engine().SurfaceIDs() {
			if strings.HasPrefix(id, "form-") {
				surfID = id
			}
		}
	}
	deadline := time.Now().Add(e2eTimeout)
	for surfID == "" && time.Now().Before(deadline) {
		select {
		case ev := <-sess.Events():
			if e, ok := ev.(agent.AgentMessageEvent); ok {
				applyMessage(e.Msg)
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if surfID == "" {
		t.Fatal("form surface never arrived")
	}

	// 3. Build the interactive widget and fill the name field. The editor
	//    seeds from the fixture's data model, so typed runes append to
	//    whatever /name held when the surface arrived.
	m, err := widget.New(sess.Engine().Surface(surfID))
	if err != nil {
		t.Fatalf("widget: %v", err)
	}
	surf := sess.Engine().Surface(surfID)
	seeded, _ := jsonptr.Get(surf.DataModel, "/name")
	seed, _ := seeded.(string)
	if _, _, err := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Grace")}); err != nil {
		t.Fatalf("type: %v", err)
	}
	surf = sess.Engine().Surface(surfID)
	if v, _ := jsonptr.Get(surf.DataModel, "/name"); v != seed+"Grace" {
		t.Fatalf("/name = %#v, want %q (two-way binding failed)", v, seed+"Grace")
	}

	// 4. Focus the submit button (last focusable) and fire it.
	for m.FocusedID() != "submit-btn" {
		if _, _, err := m.Update(tea.KeyMsg{Type: tea.KeyTab}); err != nil {
			t.Fatal(err)
		}
	}
	out, consumed, err := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if err != nil || !consumed {
		t.Fatalf("activate: consumed=%v err=%v", consumed, err)
	}
	if out.Action == nil || out.Action.Name != "submit" {
		t.Fatalf("action = %+v", out.Action)
	}

	// 5. Send the action as the TUI pane does: one a2ui+json data part,
	//    continuing the taskless context, with engine metadata merged by
	//    the session.
	raw, err := json.Marshal(a2ui.RendererMessage{Version: out.EnvelopeVersion, Action: out.Action})
	if err != nil {
		t.Fatal(err)
	}
	part := a2a.NewDataPart(json.RawMessage(raw))
	part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
	msg := a2a.NewMessage(a2a.MessageRoleUser, part)
	msg.ContextID = contextID
	sess.SendRaw(ctx, msg, agent.SendOptions{ContextID: contextID})

	// 6. Wait for the agent's reply to mutate the surface, then refresh
	//    the widget exactly like the pane does on surface updates.
	updated := false
	deadline = time.Now().Add(e2eTimeout)
	for !updated && time.Now().Before(deadline) {
		select {
		case ev := <-sess.Events():
			if e, ok := ev.(agent.AgentMessageEvent); ok {
				applyMessage(e.Msg)
			}
			surf = sess.Engine().Surface(surfID)
			if surf == nil {
				t.Fatal("surface vanished after action")
			}
			if _, ok := jsonptr.Get(surf.DataModel, "/submitted"); ok {
				updated = true
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !updated {
		t.Fatal("agent never updated the surface after the action")
	}
	if err := m.Refresh(); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	view := m.View(80, 0)
	if !strings.Contains(view, "Submitted ✓") {
		t.Fatalf("agent acknowledgment missing from the refreshed view:\n%s", view)
	}
}

// TestDynamicSurfaceLiveUpdate exercises the "a2ui dynamic" demo: the
// refresh action makes the agent replace the whole data model and the
// formatString-bound text must re-render through the widget.
func TestDynamicSurfaceLiveUpdate(t *testing.T) {
	ts, err := fixtureagent.StartTest()
	if err != nil {
		t.Fatalf("fixture agent: %v", err)
	}
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), e2eTimeout)
	defer cancel()

	hc := &http.Client{Timeout: e2eTimeout}
	res, err := agent.Resolve(ctx, hc, ts.URL, agent.Auto)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	conn, err := agent.NewConn(ctx, res, hc)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	sess := agent.NewSession(conn)
	defer sess.Shutdown()

	sess.Send(ctx, "a2ui dynamic", agent.SendOptions{})

	var surfID string
	deadline := time.Now().Add(e2eTimeout)
	for surfID == "" && time.Now().Before(deadline) {
		select {
		case ev := <-sess.Events():
			if e, ok := ev.(agent.AgentMessageEvent); ok {
				chat.SplitMessage(e.Msg, sess.Engine())
				for _, id := range sess.Engine().SurfaceIDs() {
					if strings.HasPrefix(id, "dynamic-") {
						surfID = id
					}
				}
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
	if surfID == "" {
		t.Fatal("dynamic surface never arrived")
	}

	m, err := widget.New(sess.Engine().Surface(surfID))
	if err != nil {
		t.Fatalf("widget: %v", err)
	}
	if view := m.View(80, 0); !strings.Contains(view, "count=1, label=initial") {
		t.Fatalf("initial bound text missing:\n%s", view)
	}

	// Fire refresh twice.
	for i := 0; i < 2; i++ {
		out, _, err := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if err != nil || out.Action == nil || out.Action.Name != "refresh" {
			t.Fatalf("refresh %d: out=%+v err=%v", i, out.Action, err)
		}
		raw, err := json.Marshal(a2ui.RendererMessage{Version: out.EnvelopeVersion, Action: out.Action})
		if err != nil {
			t.Fatal(err)
		}
		part := a2a.NewDataPart(json.RawMessage(raw))
		part.Metadata = map[string]any{"mimeType": a2ui.MimeTypeA2UI}
		sess.SendRaw(ctx, a2a.NewMessage(a2a.MessageRoleUser, part), agent.SendOptions{})

		// Wait for the data-model replacement to land.
		surf := sess.Engine().Surface(surfID)
		deadline := time.Now().Add(e2eTimeout)
		for {
			if v, _ := jsonptr.Get(surf.DataModel, "/label"); v == "refreshed" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("refresh %d: agent update never landed (label=%v)", i, mustGet(surf.DataModel, "/label"))
			}
			select {
			case ev := <-sess.Events():
				if e, ok := ev.(agent.AgentMessageEvent); ok {
					chat.SplitMessage(e.Msg, sess.Engine())
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
		if err := m.Refresh(); err != nil {
			t.Fatal(err)
		}
	}

	if view := m.View(80, 0); !strings.Contains(view, "label=refreshed") {
		t.Fatalf("refreshed text missing:\n%s", view)
	}
}

func mustGet(model any, path string) any {
	v, _ := jsonptr.Get(model, path)
	return v
}
