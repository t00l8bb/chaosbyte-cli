package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bchayka/gitstatus/bricks"
)

// demoSeeds is a hand-picked slice of short chat-style snippets. The blitz
// widget normalises these to 5..12 chars and uses them for falling bars.
var demoSeeds = []string{
	"ship it",
	"lgtm",
	"prod is red",
	"rebase me",
	"git blame",
	"works 4 me",
	"force push",
	"hot fix",
	"rollback",
	"vibes ok",
	"main is red",
	"it compiles",
	"its a feat",
	"shrug",
	"merge it",
}

type demo struct {
	blitz *bricks.BricksBlitz
	done  bool
	final bricks.BlitzEndedMsg
}

func (d demo) Init() tea.Cmd {
	return d.blitz.Init()
}

func (d demo) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		switch m.String() {
		case "ctrl+c", "q":
			return d, tea.Quit
		}
	case bricks.BlitzEndedMsg:
		d.done = true
		d.final = m
		return d, tea.Quit
	}
	var cmd tea.Cmd
	d.blitz, cmd = d.blitz.Update(msg)
	return d, cmd
}

func (d demo) View() string {
	return d.blitz.View()
}

func main() {
	const w, h = 80, 24
	blitz := bricks.NewBricksBlitz(w, h, demoSeeds)
	d := demo{blitz: blitz}

	p := tea.NewProgram(d, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "bricks-demo: %v\n", err)
		os.Exit(1)
	}
	if fm, ok := finalModel.(demo); ok && fm.done {
		fmt.Printf("final score: %d (%d lines hit)\n", fm.final.TopScore, fm.final.Lines)
	}
}
