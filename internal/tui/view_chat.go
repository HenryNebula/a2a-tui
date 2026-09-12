package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
)

// pendingInput remembers a task awaiting a reply (input-required), so the
// next plain send attaches taskId + contextId automatically.
type pendingInput struct {
	taskID    string
	contextID string
}

// setStatus shows text on the one-line status bar above the input.
func (a *App) setStatus(text string) { a.statusText = chat.SanitizeLine(text) }

// addStatus appends a dim status block to the transcript.
func (a *App) addStatus(text string) {
	a.transcript.Append(chat.NewStatusBlock(text))
}

// addError appends an error block, decorating protocol errors with the
// last wire frame as a raw peek.
func (a *App) addError(prefix string, err error) {
	friendly := agent.FriendlyError(err)
	if prefix != "" && friendly != "" {
		friendly = prefix + ": " + friendly
	} else if prefix != "" {
		friendly = prefix + ": " + err.Error()
	}
	raw := ""
	if e := a.wireLog.Last(); e != nil && e.Err == "" {
		raw = string(e.RespBody)
	}
	a.transcript.Append(chat.NewErrorBlock(friendly, raw))
	a.setStatus(friendly)
}

// refreshTranscript pushes the rendered transcript into the viewport,
// staying glued to the bottom when the user has not scrolled up.
func (a *App) refreshTranscript() {
	atBottom := a.transcriptV.AtBottom()
	a.transcriptV.SetContent(a.transcript.Render(a.transcriptV.Width, a.transcriptV.Height))
	if atBottom {
		a.transcriptV.GotoBottom()
	}
}

// handleAgentEvent applies one session event to the app state. It
// returns the re-arm command (plus any refresh commands).
func (a *App) handleAgentEvent(ev agent.Event) tea.Cmd {
	var cmds []tea.Cmd

	switch e := ev.(type) {
	case agent.StatusLineEvent:
		a.setStatus(e.Text)

	case agent.TaskUpdateEvent:
		a.lastTaskID = e.TaskID
		if e.ContextID != "" {
			a.lastContextID = e.ContextID
		}
		statusText := e.StatusText
		if e.Source == "push" {
			// Badge webhook-delivered pills so their origin is visible in
			// the transcript itself, not just the status line.
			statusText = strings.TrimSpace("⇄ push " + statusText)
		}
		block := chat.NewTaskStateBlock(e.TaskID, e.State, statusText)
		if start, ok := a.taskStart[e.TaskID]; ok {
			block.At = start
			if e.State.Terminal() {
				block.Elapsed = time.Since(start)
				delete(a.taskStart, e.TaskID)
			}
		} else {
			now := time.Now()
			block.At = now
			// A task that arrives already terminal (one-shot blocking
			// reply) needs no duration and must not leak a start entry.
			if !e.State.Terminal() {
				a.taskStart[e.TaskID] = now
			}
		}
		a.transcript.ReplaceByID(block.ID(), block)
		if e.State.Terminal() {
			// Land the finished pill after the turn's content: the
			// state event arrives before the final message/artifact,
			// and "completed" above the reply reads backwards.
			a.transcript.MoveToEnd(block.ID())
		}
		switch {
		case e.State == a2a.TaskStateInputRequired || e.State == a2a.TaskStateAuthRequired:
			a.pending = &pendingInput{taskID: e.TaskID, contextID: e.ContextID}
			a.setPlaceholder()
			// The question rides the status line (right above the input
			// it answers); the pill stays quiet so the text is not
			// printed twice when the agent also sends it as a message.
			q := strings.TrimSpace(e.StatusText)
			if q == "" {
				q = "reply to continue"
			}
			a.setStatus("input needed · " + truncateForStatus(q))
		case e.State.Terminal():
			if a.pending != nil && a.pending.taskID == e.TaskID {
				a.pending = nil
			}
			if e.State == a2a.TaskStateCompleted {
				a.setStatus("task #" + chat.ShortID(e.TaskID) + " completed")
			}
		}

	case agent.AgentMessageEvent:
		if a.session != nil {
			// Taskless a2ui turns continue via the reply's context.
			if e.Msg.ContextID != "" {
				a.lastContextID = e.Msg.ContextID
			}
			blocks := chat.SplitMessage(e.Msg, a.session.Engine())
			for _, b := range blocks {
				// Agent replies carry the connected agent's name so they
				// read as turns, not anonymous log lines.
				if ab, ok := b.(*chat.AgentTextBlock); ok {
					ab.Name = a.agentName
				}
			}
			a.transcript.Append(blocks...)
			// A reply that lands after its task's terminal pill pulls
			// the pill below it (turn content first, verdict after).
			if id := string(e.Msg.TaskID); id != "" {
				a.transcript.MoveToEnd("task-state:" + chat.SanitizeLine(id))
			}
		}

	case agent.ArtifactEvent:
		if b := chat.NewArtifactBlock(e.TaskID, e.Artifact); b != nil {
			if e.Append {
				b.Append = true
			}
			a.transcript.ReplaceByID(b.ID(), b)
			a.transcript.MoveToEnd("task-state:" + chat.SanitizeLine(e.TaskID))
		}

	case agent.StreamCompactedEvent:
		a.addStatus("… " + strconv.Itoa(e.Dropped) + " updates compacted — refreshing task")
		if a.session != nil && e.TaskID != "" {
			cmds = append(cmds, a.fetchTask("refresh", e.TaskID, nil))
		}

	case agent.ReconnectingEvent:
		why := "stream ended early"
		if e.Err != nil {
			why = agent.FriendlyError(e.Err)
		}
		a.setStatus(why + " — reconnecting task " + e.TaskID + " (attempt " + strconv.Itoa(e.Attempt) + ")")

	case agent.StreamDoneEvent:
		if a.inflight > 0 {
			a.inflight--
		}
		if e.Err != nil {
			if a.isCancelErr(e.Err) {
				a.setStatus("stream cancelled")
			} else {
				a.addError(e.Op, e.Err)
			}
		} else {
			a.setStatus("")
		}

	case agent.ErrorEvent:
		a.addError(e.Op, e.Err)
	}

	a.syncSurfacePane()
	a.syncTasksPane()
	a.refreshTranscript()
	cmds = append(cmds, a.armListener(), a.spinnerCmd())
	return tea.Batch(cmds...)
}

// isCancelErr reports whether err is a local cancellation (Esc).
func (a *App) isCancelErr(err error) bool {
	return err != nil && (err == context.Canceled || strings.Contains(err.Error(), "context canceled"))
}

// truncateForStatus clips a status-line tail so the bar stays one line.
func truncateForStatus(s string) string {
	runes := []rune(s)
	if len(runes) <= 60 {
		return s
	}
	return string(runes[:59]) + "…"
}

// sendText submits a chat turn through the session (blocking or
// streaming per the /stream toggle), attaching any pending input task.
func (a *App) sendText(text string) tea.Cmd {
	if a.session == nil {
		a.addStatus("not connected — /connect <url-or-name> first")
		a.refreshTranscript()
		return nil
	}
	opts := agent.SendOptions{}
	reply := ""
	if a.pending != nil {
		opts.TaskID = a.pending.taskID
		opts.ContextID = a.pending.contextID
		reply = a.pending.taskID
		a.pending = nil
		a.setPlaceholder()
	}
	ub := chat.NewUserBlock(text)
	ub.ReplyTo = reply
	a.transcript.Append(ub)
	a.refreshTranscript()
	a.inflight++
	if a.streamMode {
		a.session.SendStreaming(context.Background(), text, opts)
	} else {
		a.session.Send(context.Background(), text, opts)
	}
	return a.spinnerCmd()
}

// spinnerCmd keeps the spinner ticking while work is in flight.
func (a *App) spinnerCmd() tea.Cmd {
	if a.inflight > 0 {
		return a.spinner.Tick
	}
	return nil
}

// fetchTask runs a GetTask RPC as a tea.Cmd.
func (a *App) fetchTask(op, id string, historyLength *int) tea.Cmd {
	session := a.session
	return func() tea.Msg {
		task, err := session.Conn().GetTask(context.Background(), id, historyLength)
		return taskResultMsg{op: op, id: id, task: task, err: err}
	}
}

// handleTaskResult renders a one-shot task RPC outcome.
func (a *App) handleTaskResult(m taskResultMsg) {
	if m.err != nil {
		a.addError(m.op, m.err)
		a.refreshTranscript()
		return
	}
	a.lastTaskID = m.id
	blocks := chat.BlocksForTask(m.task, a.engine())
	for _, b := range blocks {
		if ab, ok := b.(*chat.AgentTextBlock); ok {
			ab.Name = a.agentName
		}
		if tb, ok := b.(*chat.TaskStateBlock); ok {
			if start, ok := a.taskStart[tb.TaskID]; ok {
				tb.At = start
			}
		}
	}
	// The state pill replaces any live pill for the same task; history
	// and artifacts append.
	for _, b := range blocks {
		if strings.HasPrefix(b.ID(), "task-state:") {
			a.transcript.ReplaceByID(b.ID(), b)
		} else {
			a.transcript.Append(b)
		}
	}
	// The tasks dashboard shows the same detail view for its selection.
	a.tasksPane.SetDetail(m.task, a.engine())
	switch m.op {
	case "cancel":
		a.setStatus("cancel requested for task #" + chat.ShortID(m.id))
	case "refresh":
		a.setStatus("task #" + chat.ShortID(m.id) + " refreshed")
	case "task", "history":
		// Replace the "fetching…" status instead of leaving it stale.
		a.setStatus("task #" + chat.ShortID(m.id) + " loaded")
	}
	a.syncTasksPane()
	a.syncSurfacePane()
	a.refreshTranscript()
}

// engine returns the session's A2UI engine (nil without a session).
func (a *App) engine() chat.A2UIApplier {
	if a.session == nil {
		return nil
	}
	return a.session.Engine()
}

// ---------------------------------------------------------------------------
// Slash commands implemented in view_chat.go
// ---------------------------------------------------------------------------

// cmdStream toggles streaming sends. /stream on against a card that
// declares no streaming is allowed — the agent's -32004
// UnsupportedOperation (if any) is rendered by name on send.
func (a *App) cmdStream(args []string) tea.Cmd {
	if len(args) == 0 {
		state := "off"
		if a.streamMode {
			state = "on"
		}
		cap := "no"
		if a.conn != nil && a.conn.Summary.Capabilities.Streaming {
			cap = "yes"
		}
		a.addStatus("stream " + state + " (card declares streaming: " + cap + ")")
		a.refreshTranscript()
		return nil
	}
	switch args[0] {
	case "on":
		a.streamMode = true
		a.addStatus("streaming sends enabled")
	case "off":
		a.streamMode = false
		a.addStatus("blocking sends enabled")
	default:
		a.addStatus("usage: /stream [on|off]")
	}
	a.refreshTranscript()
	return nil
}

// cmdWire toggles wire capture; without an argument it also opens the
// wire pane (frames are kept in a ring regardless while capture is on).
func (a *App) cmdWire(args []string) tea.Cmd {
	if len(args) == 0 {
		a.wireLog.SetEnabled(true)
		a.openWirePane()
		a.addStatus("wire capture on · pane open (" + strconv.Itoa(len(a.wireLog.Snapshot())) + " frames buffered)")
		a.refreshTranscript()
		return nil
	}
	switch args[0] {
	case "on":
		a.wireLog.SetEnabled(true)
		a.openWirePane()
		a.addStatus("wire capture on · pane open")
	case "off":
		a.wireLog.SetEnabled(false)
		a.addStatus("wire capture off (buffered frames are kept)")
	default:
		a.addStatus("usage: /wire [on|off]")
	}
	a.refreshTranscript()
	return nil
}

// cmdPush implements /push on|off: boots (or reuses) the local webhook
// server and registers a TaskPushNotificationConfig for every task
// observed from then on. Remote agents cannot reach a loopback webhook —
// point them at a tunnel via --push-public-url.
func (a *App) cmdPush(args []string) tea.Cmd {
	if a.session == nil {
		a.addStatus("not connected — /connect <url-or-name> first")
		a.refreshTranscript()
		return nil
	}
	state := func() string {
		if a.session.PushEnabled() {
			return "on"
		}
		return "off"
	}
	if len(args) == 0 {
		a.addStatus("push " + state() + " · usage: /push on|off")
		if url := a.session.PushURL(); url != "" {
			a.addStatus("webhook: " + url)
		}
		a.refreshTranscript()
		return nil
	}
	switch args[0] {
	case "on":
		url, err := a.session.StartPush(a.pushPublicURL)
		if err != nil {
			a.addError("push", err)
			a.refreshTranscript()
			return nil
		}
		a.session.SetPushEnabled(true)
		a.addStatus("push on · webhook " + url)
		a.addStatus("new tasks are registered automatically; events arrive badged ⇄ push")
		if a.pushPublicURL == "" {
			a.addStatus("remote agents need a tunnel to reach this webhook (--push-public-url)")
		}
	case "off":
		a.session.SetPushEnabled(false)
		a.session.StopPush()
		a.addStatus("push off (webhook stopped)")
	default:
		a.addStatus("usage: /push [on|off]")
	}
	a.refreshTranscript()
	return nil
}

// cmdTask fetches a task snapshot (+history) into the transcript.
func (a *App) cmdTask(args []string) tea.Cmd {
	if a.session == nil {
		a.addStatus("not connected")
		a.refreshTranscript()
		return nil
	}
	id := a.taskIDArg(args, "/task <id>")
	if id == "" {
		return nil
	}
	a.setStatus("fetching task #" + chat.ShortID(id) + "…")
	return a.fetchTask("task", id, nil)
}

// cmdHistory fetches a task with an explicit history length.
func (a *App) cmdHistory(args []string) tea.Cmd {
	if a.session == nil {
		a.addStatus("not connected")
		a.refreshTranscript()
		return nil
	}
	if len(args) == 0 {
		a.addStatus("usage: /history <id> [n]")
		a.refreshTranscript()
		return nil
	}
	id := args[0]
	var n *int
	if len(args) > 1 {
		if v, err := strconv.Atoi(args[1]); err == nil && v >= 0 {
			n = &v
		} else {
			a.addStatus("usage: /history <id> [n] — n must be a number")
			a.refreshTranscript()
			return nil
		}
	}
	a.setStatus("fetching history for #" + chat.ShortID(id) + "…")
	return a.fetchTask("history", id, n)
}

// cmdCancel cancels a task by ID (or the most recent one).
func (a *App) cmdCancel(args []string) tea.Cmd {
	if a.session == nil {
		a.addStatus("not connected")
		a.refreshTranscript()
		return nil
	}
	id := a.taskIDArg(args, "/cancel <id>")
	if id == "" {
		return nil
	}
	conn := a.session.Conn()
	a.setStatus("canceling task #" + chat.ShortID(id) + "…")
	return func() tea.Msg {
		task, err := conn.CancelTask(context.Background(), id)
		return taskResultMsg{op: "cancel", id: id, task: task, err: err}
	}
}

// taskIDArg resolves a task ID argument, falling back to the most
// recently observed task. Empty string means "show usage".
func (a *App) taskIDArg(args []string, usage string) string {
	if len(args) > 0 {
		return args[0]
	}
	if a.lastTaskID != "" {
		return a.lastTaskID
	}
	a.addStatus("usage: " + usage)
	a.refreshTranscript()
	return ""
}

// cmdClear empties the transcript.
func (a *App) cmdClear() tea.Cmd {
	a.transcript = chat.NewTranscript()
	a.refreshTranscript()
	return nil
}
