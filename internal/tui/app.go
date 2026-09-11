// Package tui implements the a2a-tui terminal application.
//
// App is the single root Bubble Tea model. Sub-views (chat transcript, task
// dashboard, agent card, raw console, A2UI surface) are plain structs owned
// by App with Update(msg tea.Msg) and View(width, height int) string
// methods — keeping one source of truth and trivial focus routing.
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const inputRows = 3

// App is the root application model.
type App struct {
	width, height int

	// agentURL is the requested agent (flag or /connect); empty until set.
	agentURL string

	// Header state.
	agentName string
	protoVer  string
	connState string // "", "connecting", "connected", "error"

	transcript viewport.Model
	input      textarea.Model
	help       help.Model

	ready bool
}

// New returns the root application model. agentURL may be empty.
func New(agentURL string) *App {
	vp := viewport.New(0, 0)
	vp.SetContent(welcomeText())

	ta := textarea.New()
	ta.Placeholder = "Message the agent…  (/help for commands)"
	ta.Prompt = "┃ "
	ta.CharLimit = 0
	ta.SetHeight(inputRows)
	ta.Focus()

	return &App{
		agentURL:   agentURL,
		connState:  "",
		transcript: vp,
		input:      ta,
		help:       help.New(),
	}
}

func welcomeText() string {
	return styleDim.Render("a2a-tui — a terminal client for A2A agents.") + "\n" +
		styleDim.Render("Connect with /connect <url> or restart with --agent <url>.")
}

// Init implements tea.Model.
func (a *App) Init() tea.Cmd { return textarea.Blink }

// Update implements tea.Model.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = m.Width, m.Height
		a.layout()
		a.ready = true

	case tea.KeyMsg:
		switch {
		case keyMatches(m, keys.Quit):
			return a, tea.Quit
		case keyMatches(m, keys.Help):
			a.appendTranscript(styleDim.Render("keys: ctrl+c quit · enter send · alt+enter/ctrl+j newline · ? help"))
		case m.String() == "enter":
			cmds = append(cmds, a.submit())
			return a, tea.Batch(cmds...)
		}
	}

	var cmd tea.Cmd
	a.input, cmd = a.input.Update(msg)
	cmds = append(cmds, cmd)

	a.transcript, cmd = a.transcript.Update(msg)
	cmds = append(cmds, cmd)
	return a, tea.Batch(cmds...)
}

// submit handles Enter in the input box: slash commands, else chat send.
func (a *App) submit() tea.Cmd {
	text := strings.TrimRight(a.input.Value(), "\n")
	a.input.Reset()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		return a.runCommand(text)
	}
	a.appendTranscript(styleUser.Render("┃ you: ") + text)
	a.appendTranscript(styleDim.Render("  (not connected — chat lands with the next milestone)"))
	return nil
}

// runCommand dispatches a slash command. Extended per milestone.
func (a *App) runCommand(line string) tea.Cmd {
	fields := strings.Fields(line)
	cmd, args := fields[0], fields[1:]
	switch cmd {
	case "/help":
		a.appendTranscript(styleDim.Render("commands: /help /connect <url> /quit"))
	case "/quit", "/exit":
		return tea.Quit
	case "/connect":
		if len(args) != 1 {
			a.appendTranscript(styleError.Render("usage: /connect <agent-url-or-name>"))
			return nil
		}
		a.agentURL = args[0]
		a.connState = "connecting"
		a.appendTranscript(styleDim.Render("connecting to " + args[0] + "… (card fetch lands with the next milestone)"))
	default:
		a.appendTranscript(styleError.Render("unknown command: " + cmd))
	}
	return nil
}

// appendTranscript adds a line to the transcript and keeps it glued to the
// bottom when the user has not scrolled up.
func (a *App) appendTranscript(line string) {
	atBottom := a.transcript.AtBottom()
	cur := a.transcript.View()
	a.transcript.SetContent(cur + strings.TrimRight(line, "\n") + "\n")
	if atBottom {
		a.transcript.GotoBottom()
	}
}

// layout recomputes sub-view geometry after a resize.
func (a *App) layout() {
	if a.width == 0 || a.height == 0 {
		return
	}
	inputHeight := inputRows + 2 // + borders
	a.input.SetWidth(a.width - 4)
	a.transcript.Width = a.width - 2
	a.transcript.Height = a.height - inputHeight - 2 // header + help
}

// View implements tea.Model.
func (a *App) View() string {
	if !a.ready {
		return "loading…"
	}
	header := a.headerView()
	body := a.transcript.View()
	input := styleInputBox.Render(a.input.View())
	footer := a.helpView()
	gap := lipgloss.NewStyle().Height(1).Render("")
	return lipgloss.JoinVertical(lipgloss.Left, header, body, gap, input, footer)
}

func (a *App) headerView() string {
	left := "a2a-tui"
	if a.agentName != "" {
		left += " · " + a.agentName
	}
	if a.protoVer != "" {
		left += " · A2A " + a.protoVer
	}
	state := a.connState
	if state == "" {
		state = "disconnected"
	}
	renderedLeft := styleHeader.Render(left)
	right := styleState(state).Render(" " + state + " ")
	gap := strings.Repeat(" ", max(1, a.width-lipgloss.Width(renderedLeft)-lipgloss.Width(right)))
	return renderedLeft + gap + right
}

func (a *App) helpView() string {
	return styleHelp.Render(a.help.ShortHelpView([]key.Binding{
		keys.Send, keys.Help, keys.Quit,
	}))
}
