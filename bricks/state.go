// Package bricks implements the 30-second bricks blitz minigame. It is
// self-contained: the host wires Init/Update/View and watches for
// BlitzEndedMsg, nothing else.
package bricks

import (
	"math/rand"
	"time"
)

// Playfield geometry. The simulation always runs on this fixed grid so
// scoring stays consistent regardless of terminal size; the renderer
// centres the grid inside whatever space the host gives it.
const (
	Cols       = 60
	Rows       = 18
	Duration   = 30 * time.Second
	SpawnEvery = 1200 * time.Millisecond
	FallCPS    = 2.0 // cells per second
	RiseCells  = 4
	ExplodeMs  = 200
	PaddleW    = 12
	FloorSlots = 3
)

type barPhase int

const (
	phaseFalling barPhase = iota
	phaseRising
	phaseExploding
)

type bar struct {
	text      string
	x         int
	y         float64
	phase     barPhase
	riseLeft  int
	explodeAt time.Time
	particles []particle
}

func (b *bar) width() int { return len(b.text) }
func (b *bar) row() int   { return int(b.y) }

type particle struct {
	glyph byte
	x     int
	y     int
}

// floorEntry is a bar that touched the floor. Each one occupies one of the
// three slots and stays on screen as a visible toll.
type floorEntry struct {
	text string
	x    int
}

type phase int

const (
	phaseReady phase = iota
	phaseRunning
	phaseOver
)

type state struct {
	phase phase

	startedAt time.Time
	lastTick  time.Time
	lastSpawn time.Time

	paddleX int

	bars  []*bar
	floor []floorEntry

	score   int
	hits    int
	rng     *rand.Rand
	seeds   []string
	seedIdx int
}

func newState(seedLines []string) *state {
	return &state{
		phase:   phaseReady,
		paddleX: (Cols - PaddleW) / 2,
		seeds:   trimSeeds(seedLines),
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// trimSeeds normalises seed strings to the 5..12 char range the renderer
// expects. Longer entries are truncated, shorter ones are dropped.
func trimSeeds(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if len(s) < 5 {
			continue
		}
		if len(s) > 12 {
			s = s[:12]
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		out = defaultSeeds()
	}
	return out
}

func defaultSeeds() []string {
	return []string{
		"ship it",
		"lgtm",
		"works 4 me",
		"rebase me",
		"git blame",
		"prod is ok",
		"it compiles",
		"merge it",
		"hot fix",
		"rollback",
		"force push",
		"its a feat",
		"vibes ok",
		"shrug",
		"main is red",
	}
}

func (s *state) start(now time.Time) {
	s.phase = phaseRunning
	s.startedAt = now
	s.lastTick = now
	s.lastSpawn = now.Add(-SpawnEvery)
}

func (s *state) elapsed(now time.Time) time.Duration {
	if s.phase == phaseReady {
		return 0
	}
	return now.Sub(s.startedAt)
}

func (s *state) remaining(now time.Time) time.Duration {
	d := Duration - s.elapsed(now)
	if d < 0 {
		return 0
	}
	return d
}

// movePaddle nudges the paddle by dx columns, clamped to the playfield.
func (s *state) movePaddle(dx int) {
	x := s.paddleX + dx
	if x < 0 {
		x = 0
	}
	if x > Cols-PaddleW {
		x = Cols - PaddleW
	}
	s.paddleX = x
}

// advance runs the simulation forward to `now`. It returns true once the
// round has ended (timer expired or floor full).
func (s *state) advance(now time.Time) bool {
	if s.phase != phaseRunning {
		return s.phase == phaseOver
	}

	dt := now.Sub(s.lastTick).Seconds()
	if dt < 0 {
		dt = 0
	}
	s.lastTick = now

	for i := 0; i < len(s.bars); i++ {
		b := s.bars[i]
		switch b.phase {
		case phaseFalling:
			b.y += FallCPS * dt
			if s.collidesWithPaddle(b) {
				b.phase = phaseRising
				b.riseLeft = RiseCells
				s.score += b.width()
				s.hits++
				continue
			}
			if b.row() >= Rows-1 {
				s.floor = append(s.floor, floorEntry{text: b.text, x: b.x})
				s.bars[i] = nil
			}
		case phaseRising:
			b.y -= FallCPS * dt
			riseDone := (Rows - 1 - b.row()) >= RiseCells
			if riseDone || b.riseLeft <= 0 || b.row() <= 0 {
				b.phase = phaseExploding
				b.explodeAt = now
				b.particles = makeParticles(b, s.rng)
			}
		case phaseExploding:
			if now.Sub(b.explodeAt) >= ExplodeMs*time.Millisecond {
				s.bars[i] = nil
			}
		}
	}
	s.bars = compactBars(s.bars)

	if now.Sub(s.lastSpawn) >= SpawnEvery {
		s.spawnBar()
		s.lastSpawn = now
	}

	if len(s.floor) >= FloorSlots {
		s.phase = phaseOver
		return true
	}
	if s.remaining(now) == 0 {
		s.phase = phaseOver
		return true
	}
	return false
}

func compactBars(in []*bar) []*bar {
	out := in[:0]
	for _, b := range in {
		if b != nil {
			out = append(out, b)
		}
	}
	return out
}

// collidesWithPaddle returns true if the bar overlaps the paddle row and
// the paddle's column span. The paddle sits on the bottom row; contact is
// registered one row above so glyphs never overlap.
func (s *state) collidesWithPaddle(b *bar) bool {
	if b.phase != phaseFalling {
		return false
	}
	if b.row() != Rows-2 {
		return false
	}
	bl, br := b.x, b.x+b.width()-1
	pl, pr := s.paddleX, s.paddleX+PaddleW-1
	return br >= pl && bl <= pr
}

func (s *state) spawnBar() {
	if len(s.seeds) == 0 {
		return
	}
	if s.rng.Intn(3) == 0 {
		s.seedIdx = s.rng.Intn(len(s.seeds))
	}
	txt := s.seeds[s.seedIdx%len(s.seeds)]
	s.seedIdx++

	maxX := Cols - len(txt)
	if maxX < 0 {
		return
	}
	x := 0
	if maxX > 0 {
		x = s.rng.Intn(maxX + 1)
	}
	s.bars = append(s.bars, &bar{
		text:  txt,
		x:     x,
		y:     0,
		phase: phaseFalling,
	})
}

// makeParticles scatters the bar's glyphs around its current row. Particles
// are visual only; the simulation does not interact with them after spawn.
func makeParticles(b *bar, rng *rand.Rand) []particle {
	out := make([]particle, 0, len(b.text))
	for i := 0; i < len(b.text); i++ {
		c := b.text[i]
		if c == ' ' {
			continue
		}
		dx := rng.Intn(5) - 2
		dy := rng.Intn(3) - 1
		x := b.x + i + dx
		y := b.row() + dy
		if x < 0 {
			x = 0
		}
		if x >= Cols {
			x = Cols - 1
		}
		if y < 0 {
			y = 0
		}
		if y >= Rows {
			y = Rows - 1
		}
		out = append(out, particle{glyph: c, x: x, y: y})
	}
	return out
}
