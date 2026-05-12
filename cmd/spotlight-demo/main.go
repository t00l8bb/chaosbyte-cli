package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func seedDemoCandidates() []Candidate {
	return []Candidate{
		{Nick: "@yamlhater", Bio: "CI whisperer, ships on red."},
		{Nick: "@nullpointer", Bio: "writes pointers, dereferences feelings."},
		{Nick: "@vibe_master", Bio: "the landing page IS the product."},
		{Nick: "@devops_bard", Bio: "the changelog reads like a confession."},
		{Nick: "@junior_dev", Bio: "is this the one where you press a key and it just works?"},
	}
}

type demoModel struct {
	engine *SpotlightEngine

	width  int
	height int

	log []string
}

func newDemoModel() demoModel {
	return demoModel{
		engine: NewSpotlightEngine(seedDemoCandidates()),
	}
}

func (m demoModel) Init() tea.Cmd { return m.engine.Init() }

func (m demoModel) appendLog(s string) demoModel {
	stamp := time.Now().Format("15:04:05.000")
	m.log = append(m.log, lipgloss.NewStyle().Foreground(colorMuted).Render(stamp)+"  "+s)
	if len(m.log) > 40 {
		m.log = m.log[len(m.log)-40:]
	}
	return m
}

func (m demoModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		}
		if cmd, ok := m.engine.HandleKey(msg.String()); ok {
			return m, cmd
		}
	case SpotlightQueuedMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorAccent).Render("queued ") + msg.Candidate.Nick)
	case OptInPromptedMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorAccent2).Render("opt-in ") + msg.Candidate.Nick +
			" deadline " + msg.Deadline.Format("15:04:05"))
	case OptInChosenMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorOk).Render("chose ") +
			msg.Candidate.Nick + fmt.Sprintf(" (%d)", msg.Choice))
	case OptInTimeoutMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorMuted).Render("skip   ") + msg.Candidate.Nick)
	case SpotlightStartedMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorOk).Render("present ") + msg.Spotlight.Project)
	case SpotlightEndedMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorMuted).Render("ended  ") + msg.Spotlight.Project)
	case TransitionStartedMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("transition..."))
	case TransitionCompleteMsg:
		m = m.appendLog(lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("transition done"))
	}

	eng, cmd := m.engine.Update(msg)
	m.engine = eng
	return m, cmd
}

func (m demoModel) View() string {
	if m.width == 0 {
		return ""
	}
	header := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).
		Render("spotlight engine demo") + "  " +
		lipgloss.NewStyle().Foreground(colorMuted).
			Render("state: "+m.engine.State()+"  ·  q to quit  ·  1-4 during opt-in")

	var center string
	if cur := m.engine.Current(); cur != nil {
		center = renderPresenting(*cur, m.width)
	} else if prompt := m.engine.RenderPrompt(m.width); prompt != "" {
		center = prompt
	} else {
		center = lipgloss.NewStyle().Foreground(colorMuted).Italic(true).
			Render("idle / transitioning...")
	}

	logTitle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("events")
	logBody := strings.Join(m.log, "\n")

	body := lipgloss.JoinVertical(lipgloss.Left,
		header,
		"",
		center,
		"",
		logTitle,
		logBody,
	)
	return body
}

func renderPresenting(sp Spotlight, width int) string {
	innerW := width - 6
	if innerW < 30 {
		innerW = 30
	}
	title := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true).Render(sp.Project)
	author := lipgloss.NewStyle().Foreground(colorOk).Render(sp.Author)
	desc := lipgloss.NewStyle().Foreground(colorFg).Render(sp.Description)
	var highlights []string
	for _, h := range sp.Highlights {
		highlights = append(highlights,
			lipgloss.NewStyle().Foreground(colorAccent).Render("  > ")+
				lipgloss.NewStyle().Foreground(colorFg).Render(h))
	}
	content := strings.Join([]string{
		title,
		author,
		"",
		desc,
		"",
		strings.Join(highlights, "\n"),
	}, "\n")
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent2).
		Padding(0, 2).
		Width(innerW).
		Render(content)
}

func main() {
	p := tea.NewProgram(newDemoModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "spotlight-demo: %v\n", err)
		os.Exit(1)
	}
}
