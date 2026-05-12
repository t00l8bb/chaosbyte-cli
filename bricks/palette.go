package bricks

import "github.com/charmbracelet/lipgloss"

// The palette mirrors the Tokyo Night values defined in the root styles.go.
// We restate the hex codes here so the subpackage can render without
// reaching back into package main; the values are intentionally identical.
var (
	colorBg       = lipgloss.Color("#1a1b26")
	colorFg       = lipgloss.Color("#c0caf5")
	colorMuted    = lipgloss.Color("#565f89")
	colorAccent   = lipgloss.Color("#7aa2f7")
	colorAccent2  = lipgloss.Color("#bb9af7")
	colorOk       = lipgloss.Color("#9ece6a")
	colorWarn     = lipgloss.Color("#e0af68")
	colorLike     = lipgloss.Color("#f7768e")
	colorBorderHi = lipgloss.Color("#7aa2f7")
	colorBorderLo = lipgloss.Color("#3b4261")
)
