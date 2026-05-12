package bricks

import (
	"testing"
	"time"
)

func TestCollidesWithPaddle(t *testing.T) {
	type tc struct {
		name    string
		barX    int
		barW    int // bar text length
		barRow  int
		paddleX int
		phase   barPhase
		want    bool
	}
	cases := []tc{
		{"centred hit", 20, 8, Rows - 2, 22, phaseFalling, true},
		{"left edge hit", 0, 5, Rows - 2, 0, phaseFalling, true},
		{"right edge hit", Cols - 5, 5, Rows - 2, Cols - PaddleW, phaseFalling, true},
		{"miss to the right", 50, 6, Rows - 2, 0, phaseFalling, false},
		{"miss to the left", 0, 6, Rows - 2, 40, phaseFalling, false},
		{"wrong row", 20, 8, 5, 22, phaseFalling, false},
		{"already rising", 20, 8, Rows - 2, 22, phaseRising, false},
		{"exploding", 20, 8, Rows - 2, 22, phaseExploding, false},
		{"touching paddle left edge", 18, 5, Rows - 2, 22, phaseFalling, true},
		{"one column gap left", 16, 5, Rows - 2, 22, phaseFalling, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newState(nil)
			s.paddleX = c.paddleX
			text := make([]byte, c.barW)
			for i := range text {
				text[i] = 'x'
			}
			b := &bar{
				text:  string(text),
				x:     c.barX,
				y:     float64(c.barRow),
				phase: c.phase,
			}
			got := s.collidesWithPaddle(b)
			if got != c.want {
				t.Fatalf("collidesWithPaddle: got %v want %v (barX=%d w=%d row=%d paddleX=%d phase=%d)",
					got, c.want, c.barX, c.barW, c.barRow, c.paddleX, c.phase)
			}
		})
	}
}

func TestPaddleClamping(t *testing.T) {
	s := newState(nil)
	s.paddleX = 0
	s.movePaddle(-10)
	if s.paddleX != 0 {
		t.Fatalf("paddle should clamp to 0, got %d", s.paddleX)
	}
	s.movePaddle(9999)
	if s.paddleX != Cols-PaddleW {
		t.Fatalf("paddle should clamp to %d, got %d", Cols-PaddleW, s.paddleX)
	}
}

func TestTrimSeeds(t *testing.T) {
	in := []string{
		"abc",     // too short
		"ship it", // ok
		"this string is too long and should be cut", // truncated
		"",         // dropped
		"lgtm now", // ok
	}
	out := trimSeeds(in)
	for _, s := range out {
		if len(s) < 5 || len(s) > 12 {
			t.Fatalf("seed out of bounds: %q (len=%d)", s, len(s))
		}
	}
	// Empty input must fall through to defaults.
	if d := trimSeeds(nil); len(d) == 0 {
		t.Fatal("expected default seeds when input is empty")
	}
}

func TestEndOnTimeout(t *testing.T) {
	s := newState([]string{"ship it"})
	now := time.Now()
	s.start(now)
	over := s.advance(now.Add(Duration + time.Second))
	if !over {
		t.Fatal("expected round to end after duration elapses")
	}
	if s.phase != phaseOver {
		t.Fatalf("phase = %d, want phaseOver", s.phase)
	}
}

func TestEndOnFloorFull(t *testing.T) {
	s := newState([]string{"ship it"})
	now := time.Now()
	s.start(now)
	for i := 0; i < FloorSlots; i++ {
		s.floor = append(s.floor, floorEntry{text: "x", x: 0})
	}
	over := s.advance(now.Add(50 * time.Millisecond))
	if !over {
		t.Fatal("expected round to end when floor is full")
	}
}

func TestScoringOnHit(t *testing.T) {
	s := newState([]string{"ship it"})
	now := time.Now()
	s.start(now)
	s.paddleX = 10
	b := &bar{
		text:  "ship it",
		x:     12,
		y:     float64(Rows - 2),
		phase: phaseFalling,
	}
	s.bars = []*bar{b}
	// Advance one frame; the bar already sits on the collision row.
	s.advance(now.Add(10 * time.Millisecond))
	if s.score != len("ship it") {
		t.Fatalf("score = %d, want %d", s.score, len("ship it"))
	}
	if s.hits != 1 {
		t.Fatalf("hits = %d, want 1", s.hits)
	}
	if b.phase != phaseRising {
		t.Fatalf("bar phase = %d, want phaseRising", b.phase)
	}
}
