package tui

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/HenryNebula/a2a-tui/internal/agent"
	"github.com/HenryNebula/a2a-tui/internal/chat"
)

// TasksPane is the tasks dashboard: the session registry as a table
// (id · state pill · updated-ago · title, newest first) with a cursor, plus
// a detail view that reuses the transcript's task block rendering (state
// pill, artifacts, history) in a scrollable viewport. Actions on the
// selected task are dispatched back through the App.
type TasksPane struct {
	viewport viewport.Model
	tasks    []agent.TaskMeta
	cursor   int

	// detail holds the rendered /task view of one task ("" → list mode).
	detailID     string
	detailBlocks []chat.Block
	detailCache  string
	detailWidth  int
}

// NewTasksPane returns an empty dashboard pane.
func NewTasksPane() TasksPane {
	return TasksPane{viewport: viewport.New(0, 0)}
}

// Sync pulls the current registry listing into the pane, clamping the
// cursor to the (possibly shrunken) list.
func (p *TasksPane) Sync(reg *agent.Registry) {
	if reg == nil {
		p.tasks = nil
	} else {
		p.tasks = reg.List() // most recently updated first
	}
	if p.cursor >= len(p.tasks) {
		p.cursor = max(0, len(p.tasks)-1)
	}
}

// SelectedID returns the task under the cursor ("" when the list is empty).
func (p *TasksPane) SelectedID() string {
	if p.cursor < 0 || p.cursor >= len(p.tasks) {
		return ""
	}
	return p.tasks[p.cursor].ID
}

// DetailOpen reports whether the detail view is showing.
func (p *TasksPane) DetailOpen() bool { return p.detailID != "" }

// CloseDetail returns to the list view.
func (p *TasksPane) CloseDetail() {
	p.detailID = ""
	p.detailBlocks = nil
	p.detailCache = ""
	p.viewport.GotoTop()
}

// SetDetail installs the fetched task's blocks for the detail view.
func (p *TasksPane) SetDetail(task *a2a.Task, eng chat.A2UIApplier) {
	if task == nil {
		return
	}
	p.detailID = string(task.ID)
	p.detailBlocks = chat.BlocksForTask(task, eng)
	p.detailCache = "" // re-render at next View
	p.viewport.GotoTop()
}

// Update handles one key. List mode moves the cursor; detail mode scrolls.
// Task actions (enter/c/s/r) go through the App. It returns commands and
// whether the key was consumed.
func (p *TasksPane) Update(msg tea.Msg, app *App) (tea.Cmd, bool) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil, false
	}

	if p.detailID != "" {
		switch k.String() {
		case "up", "k":
			p.viewport.ScrollUp(1)
		case "down", "j":
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

	switch k.String() {
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(p.tasks)-1 {
			p.cursor++
		}
	case "pgup":
		p.cursor = max(0, p.cursor-max(1, p.viewport.Height/2))
	case "pgdown":
		p.cursor = min(len(p.tasks)-1, p.cursor+max(1, p.viewport.Height/2))
	case "home":
		p.cursor = 0
	case "end":
		p.cursor = max(0, len(p.tasks)-1)
	case "enter":
		return p.actionDetail(app), true
	case "c":
		return p.actionCancel(app), true
	case "s":
		p.actionSubscribe(app)
		return nil, true
	case "r":
		return p.actionRefresh(app), true
	default:
		return nil, false
	}
	return nil, true
}

// actionDetail fetches the selected task for the detail view.
func (p *TasksPane) actionDetail(app *App) tea.Cmd {
	id := p.SelectedID()
	if id == "" || app == nil || app.session == nil {
		return nil
	}
	app.setStatus("fetching task " + id + "…")
	return app.fetchTask("task", id, nil)
}

// actionCancel cancels the selected task (CancelTask RPC).
func (p *TasksPane) actionCancel(app *App) tea.Cmd {
	id := p.SelectedID()
	if id == "" || app == nil || app.session == nil {
		return nil
	}
	conn := app.session.Conn()
	app.setStatus("canceling task " + id + "…")
	return func() tea.Msg {
		task, err := conn.CancelTask(context.Background(), id)
		return taskResultMsg{op: "cancel", id: id, task: task, err: err}
	}
}

// actionSubscribe resubscribes the session to the selected task.
func (p *TasksPane) actionSubscribe(app *App) {
	id := p.SelectedID()
	if id == "" || app == nil || app.session == nil {
		return
	}
	app.session.Subscribe(context.Background(), id)
	app.addStatus("subscribing to task " + id + " (reconnects with backoff)")
	app.refreshTranscript()
}

// actionRefresh re-fetches the selected task and, on A2A 1.0, lists the
// agent's tasks to discover ones this session has never seen (0.3 has no
// list method; the error is swallowed and nothing is hidden on our side).
func (p *TasksPane) actionRefresh(app *App) tea.Cmd {
	id := p.SelectedID()
	if app == nil || app.session == nil {
		return nil
	}
	reg := app.session.Registry()
	conn := app.session.Conn()
	app.setStatus("refreshing tasks…")
	listCmd := func() tea.Msg {
		res, err := conn.ListTasks(context.Background())
		if err == nil && res != nil {
			for _, task := range res.Tasks {
				reg.Observe(task)
			}
		}
		// The unsupported error (0.3) is expected: nothing to surface.
		_ = err
		return tasksRefreshedMsg{}
	}
	if id == "" {
		return listCmd
	}
	detail := app.fetchTask("task", id, nil)
	return tea.Batch(detail, listCmd)
}

// View renders the pane into the given geometry.
func (p *TasksPane) View(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if p.detailID != "" {
		return p.viewDetail(width, height)
	}
	return p.viewList(width, height)
}

// viewList renders the registry table plus the hint footer.
func (p *TasksPane) viewList(width, height int) string {
	var body string
	if len(p.tasks) == 0 {
		body = styleDim.Render("no tasks observed yet — send a message, then /tasks")
	} else {
		body = p.table(width)
	}
	footer := styleDim.Render(cell("up/down select · enter detail · c cancel · s subscribe · r refresh · esc back", width))
	bodyHeight := max(1, height-1)
	p.viewport.Width = width
	p.viewport.Height = bodyHeight
	p.viewport.SetContent(body)
	return p.viewport.View() + "\n" + footer
}

// table renders the task rows: id · state pill · updated-ago · title.
func (p *TasksPane) table(width int) string {
	const gap = "  "
	idW, stW, agW := 16, 14, 8
	titleW := width - 2 - (idW + stW + agW + 3*len(gap))
	if titleW < 8 {
		// Narrow terminal: shrink the id column before losing the title.
		idW = max(6, idW-(8-titleW))
		titleW = max(4, width-2-(idW+stW+agW+3*len(gap)))
	}

	var b strings.Builder
	head := styleCardHeader.Render(fitCell("id", idW)) + gap +
		styleCardHeader.Render(fitCell("state", stW)) + gap +
		styleCardHeader.Render(fitCell("updated", agW)) + gap +
		styleCardHeader.Render(fitCell("title", titleW))
	b.WriteString("  " + head + "\n")

	now := time.Now()
	for i, m := range p.tasks {
		marker := "  "
		if i == p.cursor {
			marker = "► "
		}
		state := styleTaskState(m.State).Render(fitCell(chat.StateLabel(m.State), stW))
		ago := fitCell(updatedAgo(now.Sub(m.UpdatedAt)), agW)
		title := fitCell(m.Title, titleW)
		if m.Terminal() {
			ago = styleDim.Render(ago)
			title = styleDim.Render(title)
		}
		row := styleCardValue.Render(fitCell(marker+m.ID, idW+2)) + gap + state + gap +
			ago + gap + title
		if i == p.cursor {
			row = lipgloss.NewStyle().Bold(true).Render(row)
		}
		b.WriteString("  " + row + "\n")
	}
	return b.String()
}

// viewDetail renders the fetched task with the transcript's block set.
func (p *TasksPane) viewDetail(width, height int) string {
	if p.detailCache == "" || p.detailWidth != width {
		p.detailWidth = width
		parts := make([]string, 0, len(p.detailBlocks))
		for _, blk := range p.detailBlocks {
			parts = append(parts, blk.Render(max(20, width-2)))
		}
		p.detailCache = strings.Join(parts, "\n\n")
	}
	footer := styleDim.Render(cell("up/down scroll · esc back to list · task "+p.detailID, width))
	bodyHeight := max(1, height-1)
	p.viewport.Width = width
	p.viewport.Height = bodyHeight
	p.viewport.SetContent(p.detailCache)
	return p.viewport.View() + "\n" + footer
}

// updatedAgo renders a coarse age like "3s", "5m", "2h", "4d".
func updatedAgo(d time.Duration) string {
	switch {
	case d < 0:
		return "now"
	case d < time.Minute:
		return strconv.Itoa(int((d+time.Second/2)/time.Second)) + "s"
	case d < time.Hour:
		return strconv.Itoa(int((d+time.Minute/2)/time.Minute)) + "m"
	case d < 24*time.Hour:
		return strconv.Itoa(int((d+time.Hour/2)/time.Hour)) + "h"
	default:
		return strconv.Itoa(int((d+12*time.Hour)/(24*time.Hour))) + "d"
	}
}

// ---------------------------------------------------------------------------
// App-side glue
// ---------------------------------------------------------------------------

// tasksRefreshedMsg fires after a ListTasks refresh so the pane re-renders.
type tasksRefreshedMsg struct{}

// openTasksPane switches to the dashboard (ctrl+k, /tasks).
func (a *App) openTasksPane() {
	if a.session != nil {
		a.tasksPane.Sync(a.session.Registry())
	}
	a.pane = paneTasks
}

// syncTasksPane feeds registry updates into the dashboard after every
// session event.
func (a *App) syncTasksPane() {
	if a.session == nil {
		return
	}
	a.tasksPane.Sync(a.session.Registry())
}
