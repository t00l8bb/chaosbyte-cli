package animation

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Tokyo Night palette duplicated from the root package's styles.go so this
// package stays a leaf (the root imports animation, not the other way).
// Keep these in sync with styles.go.
var (
	colorFg       = lipgloss.Color("#c0caf5")
	colorMuted    = lipgloss.Color("#565f89")
	colorAccent   = lipgloss.Color("#7aa2f7")
	colorAccent2  = lipgloss.Color("#bb9af7")
	colorBorderLo = lipgloss.Color("#3b4261")
)

// styleFor maps a scene's style key to a lipgloss style. Keys come from the
// scene engine and are stable. Unknown keys fall through to plain fg.
func styleFor(key string) lipgloss.Style {
	switch key {
	case "bright":
		return lipgloss.NewStyle().Foreground(colorFg).Bold(true)
	case "dim":
		return lipgloss.NewStyle().Foreground(colorMuted)
	case "accent":
		return lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	case "accent2":
		return lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	case "muted":
		return lipgloss.NewStyle().Foreground(colorBorderLo)
	}
	return lipgloss.NewStyle().Foreground(colorFg)
}

// renderFrame walks the grid row by row and flushes contiguous runs of the
// same style as a single lipgloss render call. Trailing blanks on each row
// are dropped to keep the output compact.
func renderFrame(f FrameData) string {
	if f.Width == 0 || f.Height == 0 || len(f.Cells) == 0 {
		return ""
	}
	var out strings.Builder
	out.Grow(f.Width * f.Height)

	for y := 0; y < f.Height; y++ {
		row := f.Cells[y*f.Width : (y+1)*f.Width]

		end := f.Width
		for end > 0 {
			c := row[end-1]
			if c.Ch != ' ' && c.Ch != 0 {
				break
			}
			if c.Style != "" {
				break
			}
			end--
		}

		var run strings.Builder
		runStyle := ""
		flush := func() {
			if run.Len() == 0 {
				return
			}
			if runStyle == "" {
				out.WriteString(run.String())
			} else {
				out.WriteString(styleFor(runStyle).Render(run.String()))
			}
			run.Reset()
		}

		for x := 0; x < end; x++ {
			c := row[x]
			ch := c.Ch
			if ch == 0 {
				ch = ' '
			}
			if c.Style != runStyle {
				flush()
				runStyle = c.Style
			}
			run.WriteRune(ch)
		}
		flush()

		if y < f.Height-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}
