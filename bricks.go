package main

import (
	"github.com/bchayka/gitstatus/bricks"
)

// The blitz implementation lives in the bricks subpackage so the
// cmd/bricks-demo binary can import it without conflicting with the root
// package's own main(). This file re-exports the public API so the rest of
// package main can refer to BricksBlitz and friends without an import.

// BricksBlitz is the self-contained blitz widget.
type BricksBlitz = bricks.BricksBlitz

// BlitzStartedMsg is emitted once when a round begins.
type BlitzStartedMsg = bricks.BlitzStartedMsg

// BlitzEndedMsg is emitted once after the round resolves.
type BlitzEndedMsg = bricks.BlitzEndedMsg

// NewBricksBlitz constructs a fresh blitz widget. See bricks.NewBricksBlitz
// for the seed-line contract (5..12 chars).
func NewBricksBlitz(width, height int, seedLines []string) *BricksBlitz {
	return bricks.NewBricksBlitz(width, height, seedLines)
}
