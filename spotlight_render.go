package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

var (
	optInHeaderStyle = lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	optInBodyStyle   = lipgloss.NewStyle().Foreground(colorFg)
	optInKeyStyle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	optInMutedStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	optInBoxStyle    = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorBorderLo).
				Padding(0, 2)
)

var optInChoices = []struct{ label string }{
	{"share a repo"},
	{"pick a topic"},
	{"start a game blitz"},
	{"skip"},
}

func renderOptInPrompt(c Candidate, remaining time.Duration, width int) string {
	secs := int(remaining.Round(time.Second).Seconds())
	if secs < 0 {
		secs = 0
	}

	header := optInHeaderStyle.Render(c.Nick) + optInMutedStyle.Render(", you're up next in ") +
		optInKeyStyle.Render(fmt.Sprintf("%ds", secs)) + optInMutedStyle.Render(". choose:")

	rows := make([]string, 0, len(optInChoices))
	for i, ch := range optInChoices {
		num := optInKeyStyle.Render(fmt.Sprintf("%d)", i+1))
		body := optInBodyStyle.Render(ch.label)
		rows = append(rows, "  "+num+" "+body)
	}

	inner := strings.Join(append([]string{header}, rows...), "\n")
	innerW := width - 6
	if innerW < 30 {
		innerW = 30
	}
	return optInBoxStyle.Width(innerW).Render(inner)
}
