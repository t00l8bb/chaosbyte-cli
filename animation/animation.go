// Package animation is a pure-Go tick-driven scene engine for the chaosbyte
// terminal UI. A scene declares a fixed grid and frame count, the player
// drives one frame per ~33ms tick via tea.Tick, and the renderer flushes a
// cell grid to a styled string.
//
// The long-term plan is to use chenglou/pretext (a JS text-measurement
// engine) as the layout brain for animated typography. This package is the
// stand-in: same Scene contract, pure Go, no Node sidecar.
package animation

import (
	"fmt"
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Cell is one terminal grid cell. Style is a key resolved by the renderer
// (see renderFrame). Empty Ch and empty Style render as a single space.
type Cell struct {
	Ch    rune
	Style string
}

// FrameData is a row-major grid of cells. len(Cells) must equal Width*Height.
type FrameData struct {
	Width  int
	Height int
	Cells  []Cell
}

// Scene is a producer of fixed-count frames at a fixed pixel grid. Scenes are
// deterministic given the same Init params: Frame(i) for i in [0, FrameCount).
type Scene interface {
	Init(width, height int, params map[string]any) error
	FrameCount() int
	Frame(i int) FrameData
	DurationMs() int
}

// FrameTickMsg is delivered by the Player on each frame tick. The receiving
// model treats it as opaque, calls Player.Update, and re-renders.
type FrameTickMsg struct {
	PlayerID int
	Tick     int
}

// FrameMs is the per-frame interval. 33ms == ~30 fps, the minimum the spec
// calls for.
const FrameMs = 33

// playerIDSeq disambiguates concurrent players so a stale tick from a finished
// player cannot drive a new one.
var playerIDSeq int

// Player drives a Scene one frame per tick. It owns the current frame index
// and the cached current FrameData. Use NewPlayer, then wire Init/Update/Render
// into a Bubbletea model.
type Player struct {
	id       int
	scene    Scene
	idx      int
	current  FrameData
	done     bool
	interval time.Duration
}

// NewPlayer initializes the scene against the given grid and prepares a player
// at frame 0. The player is not yet running. Call Init() to start the tick
// stream.
func NewPlayer(scene Scene, width, height int, params map[string]any) (*Player, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("animation: invalid grid %dx%d", width, height)
	}
	if err := scene.Init(width, height, params); err != nil {
		return nil, err
	}
	playerIDSeq++
	p := &Player{
		id:       playerIDSeq,
		scene:    scene,
		interval: FrameMs * time.Millisecond,
	}
	if scene.FrameCount() > 0 {
		p.current = scene.Frame(0)
	}
	return p, nil
}

// Init returns the command that schedules the first tick. The model should
// include this in its Init() Cmd batch or as the result of the message that
// triggered the animation.
func (p *Player) Init() tea.Cmd {
	if p == nil || p.scene.FrameCount() == 0 {
		return nil
	}
	return p.tickCmd()
}

// Update advances the player one frame if the message is a tick for this
// player. Returns the player and the next tick command, or nil when done.
func (p *Player) Update(msg tea.Msg) (*Player, tea.Cmd) {
	if p == nil || p.done {
		return p, nil
	}
	t, ok := msg.(FrameTickMsg)
	if !ok || t.PlayerID != p.id {
		return p, nil
	}
	p.idx++
	if p.idx >= p.scene.FrameCount() {
		p.idx = p.scene.FrameCount() - 1
		p.current = p.scene.Frame(p.idx)
		p.done = true
		return p, nil
	}
	p.current = p.scene.Frame(p.idx)
	return p, p.tickCmd()
}

// Render returns the current frame as a styled string with embedded newlines.
func (p *Player) Render() string {
	if p == nil {
		return ""
	}
	return renderFrame(p.current)
}

// Done reports whether the scene has played to its final frame.
func (p *Player) Done() bool {
	if p == nil {
		return true
	}
	return p.done
}

// ID exposes the unique player identifier so callers can route FrameTickMsg
// to the right player when multiple are active.
func (p *Player) ID() int {
	if p == nil {
		return 0
	}
	return p.id
}

func (p *Player) tickCmd() tea.Cmd {
	id := p.id
	next := p.idx + 1
	return tea.Tick(p.interval, func(time.Time) tea.Msg {
		return FrameTickMsg{PlayerID: id, Tick: next}
	})
}

// blankFrame allocates a frame filled with spaces and empty style.
func blankFrame(width, height int) FrameData {
	cells := make([]Cell, width*height)
	for i := range cells {
		cells[i].Ch = ' '
	}
	return FrameData{Width: width, Height: height, Cells: cells}
}

// putRune writes a single rune at (x, y) if in bounds.
func (f *FrameData) putRune(x, y int, ch rune, style string) {
	if x < 0 || y < 0 || x >= f.Width || y >= f.Height {
		return
	}
	f.Cells[y*f.Width+x] = Cell{Ch: ch, Style: style}
}

// easeInOutCubic is the symmetric ease curve used for glyph motion. t is
// clamped to [0, 1].
func easeInOutCubic(t float64) float64 {
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	if t < 0.5 {
		return 4 * t * t * t
	}
	u := -2*t + 2
	return 1 - u*u*u/2
}

// lerp interpolates a→b by t. No clamping; caller is responsible.
func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

// roundI is the tiny helper for math.Round → int.
func roundI(v float64) int { return int(math.Round(v)) }
