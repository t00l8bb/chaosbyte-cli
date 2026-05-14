// Package monobyte is the Bubbletea TUI behind the `monobyte` CLI.
// It owns the single-pane scrollback + input field, the input
// router (plain → agent, /verb → dispatcher, !cmd → shell), and the
// async agent-step pipeline.
package monobyte

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"

	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/sandbox"
)

// Config holds the dependencies the TUI needs.
type Config struct {
	Workspace string
	Sandbox   sandbox.Sandbox
	Agent     agent.Agent
	Model     string
	BaseURL   string
}

// New returns a fresh monobyte Model.
func New(cfg Config) tea.Model {
	ti := textinput.New()
	ti.Placeholder = "talk to claude · ! shell · / verbs"
	ti.Prompt = "› "
	ti.CharLimit = 0
	ti.Focus()

	vp := viewport.New(80, 20)
	vp.MouseWheelEnabled = true

	return &Model{
		cfg:      cfg,
		input:    ti,
		scroll:   vp,
		lines:    []line{},
		thinking: false,
	}
}

// Model is the Bubbletea state.
type Model struct {
	cfg   Config
	input textinput.Model
	scroll viewport.Model
	lines []line
	width int
	height int

	// thinking is true while an agent step is in flight; the input is
	// disabled until the step returns.
	thinking bool

	// quitting flags a graceful exit so the agent goroutine can wind
	// down without panicking on a closed program.
	quitting bool
}

// line is one rendered row in the scrollback. Different kinds get
// different styling.
type line struct {
	kind lineKind
	text string
}

type lineKind int

const (
	lineSplash lineKind = iota
	lineUser
	lineAgent
	lineTool
	lineShell
	lineSystem
	lineError
)

func (m *Model) Init() tea.Cmd {
	m.append(line{kind: lineSplash, text: splashText(m.cfg)})
	return textinput.Blink
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.scroll.Width = msg.Width
		m.scroll.Height = max(1, msg.Height-3)
		m.rerender()
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyCtrlD:
			m.quitting = true
			return m, tea.Quit
		case tea.KeyEnter:
			if m.thinking {
				return m, nil
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				return m, nil
			}
			m.input.SetValue("")
			return m, m.handleInput(text)
		}

	case agentStepResultMsg:
		m.thinking = false
		m.handleAgentResult(msg)
		return m, nil

	case shellResultMsg:
		m.handleShellResult(msg)
		return m, nil

	case dispatcherResultMsg:
		m.handleDispatcherResult(msg)
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	header := titleStyle.Render("monobyte") + "  " + dimStyle.Render(m.cfg.Workspace)
	hint := dimStyle.Render(fmt.Sprintf("%s · %s", m.cfg.Model, baseURLLabel(m.cfg.BaseURL)))
	prompt := m.input.View()
	if m.thinking {
		prompt = dimStyle.Render("claude is thinking…")
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		lipgloss.JoinHorizontal(lipgloss.Left, header, "  ", hint),
		m.scroll.View(),
		prompt,
	)
}

// append adds a line and refreshes the viewport.
func (m *Model) append(l line) {
	m.lines = append(m.lines, l)
	m.rerender()
}

// rerender renders the lines into the viewport content.
func (m *Model) rerender() {
	var b strings.Builder
	for i, l := range m.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(renderLine(l))
	}
	m.scroll.SetContent(b.String())
	m.scroll.GotoBottom()
}

// renderLine formats one scrollback line.
func renderLine(l line) string {
	switch l.kind {
	case lineSplash:
		return splashStyle.Render(l.text)
	case lineUser:
		return userStyle.Render("› ") + l.text
	case lineAgent:
		return agentStyle.Render("claude  ") + "\n" + l.text
	case lineTool:
		return toolStyle.Render("  · ") + dimStyle.Render(l.text)
	case lineShell:
		return shellStyle.Render("  $ ") + l.text
	case lineSystem:
		return systemStyle.Render(l.text)
	case lineError:
		return errorStyle.Render(l.text)
	}
	return l.text
}

func splashText(cfg Config) string {
	return strings.Join([]string{
		"your terminal is monobyte. tell me what to build.",
		"",
		"  · just talk, claude is listening",
		"  · `!ls` runs a shell command in this directory",
		"  · `/help` lists power-user verbs",
		"  · ctrl+c quits",
	}, "\n")
}

func baseURLLabel(u string) string {
	if strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost") {
		return "via cliproxyapi"
	}
	return "via anthropic"
}

// styles

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#7aa2f7"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#565f89"))
	userStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#bb9af7")).Bold(true)
	agentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Bold(true)
	toolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#9ece6a"))
	shellStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0af68"))
	systemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7aa2f7")).Italic(true)
	errorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#f7768e"))
	splashStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#c0caf5"))
)

// max is a small helper since Go's builtin max requires constraint imports
// in older module graphs.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// handleInput is the input router. Returns the tea.Cmd that will
// produce the result.
func (m *Model) handleInput(text string) tea.Cmd {
	m.append(line{kind: lineUser, text: text})
	switch {
	case strings.HasPrefix(text, "!"):
		return m.runShell(strings.TrimSpace(strings.TrimPrefix(text, "!")))
	case strings.HasPrefix(text, "/"):
		return m.runDispatcher(text)
	default:
		m.thinking = true
		return m.runAgent(text)
	}
}

// stepCtx returns a context that we can extend later for cancellation
// without changing the runAgent signature.
func (m *Model) stepCtx() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}
