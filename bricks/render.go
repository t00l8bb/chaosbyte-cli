package bricks

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// styleID indexes into a per-frame style table. We tag every cell with a
// stable integer so adjacent same-style runs can be coalesced into one
// lipgloss render call. lipgloss.Style itself is not comparable (it holds a
// transform func) so we cannot compare cells directly.
type styleID int

const (
	stBlank styleID = iota
	stBar
	stRising
	stSpark
	stPaddle
	stFloor
)

// cellGrid holds one rune per playfield cell plus a parallel style-id
// buffer.
type cellGrid struct {
	runes [][]rune
	ids   [][]styleID
}

func newCellGrid(cols, rows int) *cellGrid {
	g := &cellGrid{
		runes: make([][]rune, rows),
		ids:   make([][]styleID, rows),
	}
	for r := 0; r < rows; r++ {
		row := make([]rune, cols)
		ids := make([]styleID, cols)
		for c := 0; c < cols; c++ {
			row[c] = ' '
			ids[c] = stBlank
		}
		g.runes[r] = row
		g.ids[r] = ids
	}
	return g
}

func (g *cellGrid) put(x, y int, r rune, id styleID) {
	if y < 0 || y >= len(g.runes) {
		return
	}
	row := g.runes[y]
	if x < 0 || x >= len(row) {
		return
	}
	row[x] = r
	g.ids[y][x] = id
}

func (g *cellGrid) putString(x, y int, s string, id styleID) {
	for i, r := range s {
		g.put(x+i, y, r, id)
	}
}

func styleFor(id styleID) lipgloss.Style {
	switch id {
	case stBar:
		return lipgloss.NewStyle().Foreground(colorFg).Background(colorBorderLo)
	case stRising:
		return lipgloss.NewStyle().Foreground(colorOk).Bold(true)
	case stSpark:
		return lipgloss.NewStyle().Foreground(colorWarn).Bold(true)
	case stPaddle:
		return lipgloss.NewStyle().Foreground(colorBg).Background(colorAccent).Bold(true)
	case stFloor:
		return lipgloss.NewStyle().Foreground(colorLike).Background(colorBorderLo)
	}
	return lipgloss.NewStyle().Foreground(colorMuted)
}

func (g *cellGrid) render() string {
	var sb strings.Builder
	for y, row := range g.runes {
		ids := g.ids[y]
		// Coalesce adjacent same-id cells so we emit one SGR pair per run
		// instead of one per cell.
		i := 0
		for i < len(row) {
			j := i + 1
			for j < len(row) && ids[j] == ids[i] {
				j++
			}
			sb.WriteString(styleFor(ids[i]).Render(string(row[i:j])))
			i = j
		}
		if y < len(g.runes)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

func (b *BricksBlitz) renderView() string {
	// Use the simulation's last-known clock so the timer in the HUD stays
	// in sync with the bar physics, even under non-realtime drivers
	// (tests, replay tools).
	now := b.state.lastTick
	if now.IsZero() {
		now = time.Now()
	}
	grid := newCellGrid(Cols, Rows)

	drawFloor(grid, b.state.floor)
	drawBars(grid, b.state.bars)
	drawPaddle(grid, b.state.paddleX)

	hud := b.hud(now)
	body := grid.render()
	footer := b.footer()

	framed := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorBorderHi).
		Padding(0, 1).
		Render(lipgloss.JoinVertical(lipgloss.Left, hud, body, footer))

	w, h := b.width, b.height
	if w <= 0 {
		w = Cols + 4
	}
	if h <= 0 {
		h = Rows + 6
	}

	if b.ended {
		card := b.endCard()
		return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, card)
	}
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, framed)
}

func (b *BricksBlitz) hud(now time.Time) string {
	scoreS := lipgloss.NewStyle().Foreground(colorOk).Bold(true)
	timerS := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	titleS := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	left := scoreS.Render(fmt.Sprintf("SCORE %04d", b.state.score))
	mid := titleS.Render("bricks blitz")
	remaining := b.state.remaining(now)
	if b.state.phase == phaseReady {
		remaining = Duration
	}
	right := timerS.Render(fmt.Sprintf("%05.2fs", remaining.Seconds()))

	pad := Cols - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right)
	if pad < 2 {
		pad = 2
	}
	leftPad := pad / 2
	rightPad := pad - leftPad
	return left + strings.Repeat(" ", leftPad) + mid + strings.Repeat(" ", rightPad) + right
}

func (b *BricksBlitz) footer() string {
	help := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)
	floorTxt := fmt.Sprintf("floor %d/%d", len(b.state.floor), FloorSlots)
	return help.Render("left/right move, h/l also, floor full ends round, " + floorTxt)
}

func drawBars(g *cellGrid, bars []*bar) {
	for _, b := range bars {
		switch b.phase {
		case phaseFalling:
			g.putString(b.x, b.row(), b.text, stBar)
		case phaseRising:
			g.putString(b.x, b.row(), b.text, stRising)
		case phaseExploding:
			for _, p := range b.particles {
				g.put(p.x, p.y, rune(p.glyph), stSpark)
			}
		}
	}
}

func drawPaddle(g *cellGrid, x int) {
	y := Rows - 1
	for i := 0; i < PaddleW; i++ {
		g.put(x+i, y, '=', stPaddle)
	}
}

func drawFloor(g *cellGrid, floor []floorEntry) {
	y := Rows - 1
	for _, fe := range floor {
		x := fe.x
		if x+len(fe.text) > Cols {
			x = Cols - len(fe.text)
		}
		if x < 0 {
			x = 0
		}
		g.putString(x, y, fe.text, stFloor)
	}
}

func (b *BricksBlitz) endCard() string {
	titleS := lipgloss.NewStyle().Foreground(colorAccent2).Bold(true)
	scoreS := lipgloss.NewStyle().Foreground(colorOk).Bold(true)
	hitsS := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	reason := "time"
	if len(b.state.floor) >= FloorSlots {
		reason = "floor full"
	}

	lines := []string{
		titleS.Render("blitz over"),
		"",
		scoreS.Render(fmt.Sprintf("score   %d", b.state.score)),
		hitsS.Render(fmt.Sprintf("lines   %d", b.state.hits)),
		muted.Render(fmt.Sprintf("ended   %s", reason)),
		"",
		muted.Render("returning control to lobby..."),
	}
	body := lipgloss.JoinVertical(lipgloss.Center, lines...)
	return lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(colorAccent).
		Padding(1, 4).
		Render(body)
}
