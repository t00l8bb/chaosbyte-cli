package bricks

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestHeadlessRound walks a full round forward by feeding manufactured
// blitzTickMsgs into Update. It verifies that:
//   - the game eventually ends
//   - View() returns a non-empty string at every step
//   - the final score never decreases mid-round
//   - BlitzEndedMsg is the terminal command
func TestHeadlessRound(t *testing.T) {
	b := NewBricksBlitz(80, 24, []string{
		"ship it", "lgtm", "rebase me", "git blame", "rollback",
	})

	// Kick the start command list so the initial BlitzStartedMsg is consumed.
	if cmd := b.Init(); cmd == nil {
		t.Fatal("Init returned nil cmd")
	}

	now := time.Now()
	lastScore := 0
	var endedMsg *BlitzEndedMsg

	for step := 0; step < 2000; step++ {
		now = now.Add(blitzFrameRate)
		var cmd tea.Cmd
		b, cmd = b.Update(blitzTickMsg(now))
		if cmd != nil {
			if msg := cmd(); msg != nil {
				if e, ok := msg.(BlitzEndedMsg); ok {
					endedMsg = &e
					break
				}
			}
		}
		if v := b.View(); v == "" || !strings.Contains(v, "bricks blitz") {
			t.Fatalf("View returned no HUD at step %d", step)
		}
		score, _ := b.Score()
		if score < lastScore {
			t.Fatalf("score decreased: %d -> %d", lastScore, score)
		}
		lastScore = score
	}

	if endedMsg == nil {
		t.Fatal("game did not end within 2000 frames")
	}
	if !b.Done() {
		t.Fatal("Done() returned false after BlitzEndedMsg")
	}
	score, lines := b.Score()
	if endedMsg.TopScore != score {
		t.Fatalf("BlitzEndedMsg.TopScore=%d but Score()=%d", endedMsg.TopScore, score)
	}
	if endedMsg.Lines != lines {
		t.Fatalf("BlitzEndedMsg.Lines=%d but Score().linesHit=%d", endedMsg.Lines, lines)
	}
}

func TestPaddleResponseToKey(t *testing.T) {
	b := NewBricksBlitz(80, 24, nil)
	b.Init()
	startX := b.state.paddleX

	b, _ = b.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune{'l'}}))
	if b.state.paddleX <= startX {
		t.Fatalf("paddle did not move right with l: %d -> %d", startX, b.state.paddleX)
	}
	b, _ = b.Update(tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune{'h'}}))
	if b.state.paddleX != startX {
		t.Fatalf("paddle did not return to start with h: want %d got %d", startX, b.state.paddleX)
	}
}
