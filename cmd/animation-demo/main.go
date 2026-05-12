// animation-demo plays the Welcome scene followed by a Transition scene
// against a fixed 80x16 grid, then exits. Run with `go run ./cmd/animation-demo`.
package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bchayka/gitstatus/animation"
)

const (
	demoWidth  = 80
	demoHeight = 16
)

type stage int

const (
	stageWelcome stage = iota
	stageTransition
	stageDone
)

type model struct {
	stage   stage
	player  *animation.Player
	initCmd tea.Cmd
}

func newModel() (*model, error) {
	p, err := animation.NewPlayer(
		animation.NewWelcomeScene(),
		demoWidth, demoHeight,
		map[string]any{"nick": "@bogdan_chayka"},
	)
	if err != nil {
		return nil, err
	}
	return &model{stage: stageWelcome, player: p, initCmd: p.Init()}, nil
}

func (m *model) Init() tea.Cmd {
	c := m.initCmd
	m.initCmd = nil
	return c
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
	case animation.FrameTickMsg:
		var cmd tea.Cmd
		m.player, cmd = m.player.Update(msg)
		if m.player.Done() {
			return m.advance()
		}
		return m, cmd
	}
	return m, nil
}

func (m *model) advance() (tea.Model, tea.Cmd) {
	switch m.stage {
	case stageWelcome:
		p, err := animation.NewPlayer(
			animation.NewTransitionScene(),
			demoWidth, demoHeight,
			map[string]any{"from": "@yamlhater", "to": "@nullpointer"},
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, "demo:", err)
			return m, tea.Quit
		}
		m.stage = stageTransition
		m.player = p
		return m, p.Init()
	default:
		m.stage = stageDone
		return m, tea.Quit
	}
}

func (m *model) View() string {
	return stageLabel(m.stage) + "\n\n" + m.player.Render() + "\n"
}

func stageLabel(s stage) string {
	switch s {
	case stageWelcome:
		return "chaosbyte animation demo  ·  welcome scene"
	case stageTransition:
		return "chaosbyte animation demo  ·  spotlight transition"
	}
	return "chaosbyte animation demo  ·  done"
}

func main() {
	m, err := newModel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "demo init:", err)
		os.Exit(1)
	}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "demo run:", err)
		os.Exit(1)
	}
}
