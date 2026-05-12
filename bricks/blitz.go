package bricks

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// BlitzStartedMsg is emitted once when the round begins so the host can
// suppress its own input handling and post a chat shout if it wants.
type BlitzStartedMsg struct{}

// BlitzEndedMsg is emitted once after the round resolves. The host owns
// what happens next: back to lobby, post a score, etc.
type BlitzEndedMsg struct {
	TopScore int
	Lines    int
}

// blitzTickMsg drives the simulation forward one frame. 30ms is responsive
// enough for the 2 cells/sec fall rate without busy-rendering the terminal.
type blitzTickMsg time.Time

const blitzFrameRate = 30 * time.Millisecond

// BricksBlitz is the self-contained game widget. The host constructs one,
// pumps Init/Update/View, and watches for BlitzEndedMsg.
type BricksBlitz struct {
	width  int
	height int

	state *state
	ended bool
}

// NewBricksBlitz returns a fresh widget sized for the surrounding pane.
// seedLines feeds the bar pool; pass recent chat lines or a curated set.
// Lines outside 5..12 chars are normalised or dropped before play.
func NewBricksBlitz(width, height int, seedLines []string) *BricksBlitz {
	return &BricksBlitz{
		width:  width,
		height: height,
		state:  newState(seedLines),
	}
}

// Init starts the round. The first tickMsg arrives one frame later, at
// which point the state machine flips to running.
func (b *BricksBlitz) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return BlitzStartedMsg{} },
		blitzTick(),
	)
}

func blitzTick() tea.Cmd {
	return tea.Tick(blitzFrameRate, func(t time.Time) tea.Msg {
		return blitzTickMsg(t)
	})
}

// Update advances the simulation. The host forwards every tea.Msg here
// until Done() returns true.
func (b *BricksBlitz) Update(msg tea.Msg) (*BricksBlitz, tea.Cmd) {
	if b.ended {
		return b, nil
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width = msg.Width
		b.height = msg.Height
		return b, nil

	case blitzTickMsg:
		now := time.Time(msg)
		if b.state.phase == phaseReady {
			b.state.start(now)
		}
		over := b.state.advance(now)
		if over {
			b.ended = true
			score, lines := b.Score()
			return b, func() tea.Msg {
				return BlitzEndedMsg{TopScore: score, Lines: lines}
			}
		}
		return b, blitzTick()

	case tea.KeyMsg:
		return b.handleKey(msg)
	}
	return b, nil
}

func (b *BricksBlitz) handleKey(msg tea.KeyMsg) (*BricksBlitz, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		b.state.movePaddle(-3)
	case "right", "l":
		b.state.movePaddle(3)
	case "shift+left", "H":
		b.state.movePaddle(-6)
	case "shift+right", "L":
		b.state.movePaddle(6)
	}
	return b, nil
}

// View renders the playfield, HUD and (when applicable) the end card.
func (b *BricksBlitz) View() string { return b.renderView() }

// Done reports whether the round has finished. After Done returns true the
// host should stop forwarding messages and read the final Score.
func (b *BricksBlitz) Done() bool { return b.ended }

// Score returns the in-progress or final score, plus the number of bars hit.
func (b *BricksBlitz) Score() (points int, linesHit int) {
	return b.state.score, b.state.hits
}
