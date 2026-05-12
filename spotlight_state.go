package main

import "time"

type spotlightState int

const (
	stateIdle spotlightState = iota
	stateQueueing
	stateOptInPrompt
	statePresenting
	stateTransitioning
)

func (s spotlightState) String() string {
	switch s {
	case stateIdle:
		return "idle"
	case stateQueueing:
		return "queueing"
	case stateOptInPrompt:
		return "optInPrompt"
	case statePresenting:
		return "presenting"
	case stateTransitioning:
		return "transitioning"
	}
	return "unknown"
}

type Candidate struct {
	Nick string
	Bio  string
}

const (
	optInWindow        = 15 * time.Second
	transitionDuration = 1200 * time.Millisecond
	tickInterval       = 250 * time.Millisecond
)

// defaultDuration returns the presenting duration for a spotlight type.
// Welcomes are short, games run long, repos and topics land in the middle.
func defaultDuration(t SpotlightType) time.Duration {
	switch t {
	case SpotWelcome:
		return 20 * time.Second
	case SpotRepo:
		return 90 * time.Second
	case SpotTopic:
		return 75 * time.Second
	case SpotGame:
		return 120 * time.Second
	case SpotHelp:
		return 60 * time.Second
	case SpotShoutout:
		return 30 * time.Second
	}
	return 60 * time.Second
}

// SpotlightQueuedMsg fires when a candidate enters the queue head.
type SpotlightQueuedMsg struct{ Candidate Candidate }

// OptInPromptedMsg fires when the engine starts asking a candidate to pick.
type OptInPromptedMsg struct {
	Candidate Candidate
	Deadline  time.Time
}

// OptInChosenMsg fires when the candidate picks 1, 2, or 3.
type OptInChosenMsg struct {
	Candidate Candidate
	Choice    int
}

// OptInTimeoutMsg fires when the 15s window expires or the candidate skips.
type OptInTimeoutMsg struct{ Candidate Candidate }

// SpotlightStartedMsg fires when a spotlight enters presenting.
type SpotlightStartedMsg struct{ Spotlight Spotlight }

// SpotlightEndedMsg fires when presenting ends.
type SpotlightEndedMsg struct{ Spotlight Spotlight }

// TransitionStartedMsg fires at the start of a between-moments transition.
type TransitionStartedMsg struct{}

// TransitionCompleteMsg fires when the transition is done and the next pick is ready.
type TransitionCompleteMsg struct{}

// spotlightTickMsg is the engine's private ticker. The host should never
// construct this directly; it is delivered via the cmd returned from Update.
type spotlightTickMsg time.Time
