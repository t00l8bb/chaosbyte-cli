package main

import "github.com/charmbracelet/lipgloss"

// Local copies of the types the engine touches. The real definitions live at
// the repo root in data.go and screens_spotlight.go; we redeclare the minimum
// surface here so this demo binary compiles standalone without dragging in
// the rest of the TUI.

type Spotlight struct {
	Project     string
	Author      string
	RepoURL     string
	Description string
	Stars       int
	Language    string
	Highlights  []string
}

type SpotlightType int

const (
	SpotWelcome SpotlightType = iota
	SpotRepo
	SpotTopic
	SpotGame
	SpotHelp
	SpotShoutout
)

func (t SpotlightType) String() string {
	switch t {
	case SpotWelcome:
		return "welcome"
	case SpotRepo:
		return "repo"
	case SpotTopic:
		return "topic"
	case SpotGame:
		return "game"
	case SpotHelp:
		return "help"
	case SpotShoutout:
		return "shoutout"
	}
	return "spotlight"
}

var (
	colorFg       = lipgloss.Color("#c0caf5")
	colorMuted    = lipgloss.Color("#565f89")
	colorAccent   = lipgloss.Color("#7aa2f7")
	colorAccent2  = lipgloss.Color("#bb9af7")
	colorOk       = lipgloss.Color("#9ece6a")
	colorBorderLo = lipgloss.Color("#3b4261")
)
