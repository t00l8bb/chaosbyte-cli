package main

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// SpotlightEngine drives moment-to-moment flow for the chatroom: who is up,
// when they pick, what they show, and the gaps in between. It owns its own
// ticker so a host model only has to forward messages through Update.
type SpotlightEngine struct {
	state spotlightState

	override  []Candidate
	roundIdx  int
	roundList []Candidate

	// forcedType, when non-nil, locks the next presenting moment to a
	// specific type regardless of the opt-in choice. The moderator stream
	// sets this via Force.
	forcedType *SpotlightType

	pending  Candidate
	deadline time.Time

	current *Spotlight
	endsAt  time.Time

	transitionEndsAt time.Time

	now func() time.Time
}

// NewSpotlightEngine returns an engine seeded with a round-robin candidate list.
func NewSpotlightEngine(seedCandidates []Candidate) *SpotlightEngine {
	cands := make([]Candidate, len(seedCandidates))
	copy(cands, seedCandidates)
	return &SpotlightEngine{
		state:     stateIdle,
		roundList: cands,
		now:       time.Now,
	}
}

func (e *SpotlightEngine) Init() tea.Cmd {
	return tea.Batch(e.startQueueCmd(), e.tickCmd())
}

// Update advances the engine in response to a Bubbletea message and returns
// any follow-up command. The returned *SpotlightEngine is the same pointer;
// the signature matches Bubbletea sub-model conventions so the host can chain.
func (e *SpotlightEngine) Update(msg tea.Msg) (*SpotlightEngine, tea.Cmd) {
	switch msg := msg.(type) {
	case spotlightTickMsg:
		return e, tea.Batch(e.advanceClock(time.Time(msg)), e.tickCmd())
	case OptInChosenMsg:
		if e.state == stateOptInPrompt && msg.Candidate == e.pending {
			return e, e.startPresenting(msg.Choice)
		}
		return e, nil
	case OptInTimeoutMsg:
		if e.state == stateOptInPrompt && msg.Candidate == e.pending {
			return e, e.beginTransition()
		}
		return e, nil
	case TransitionCompleteMsg:
		if e.state == stateTransitioning {
			return e, e.startQueueCmd()
		}
		return e, nil
	}
	return e, nil
}

// HandleKey lets the host forward number keys 1-4 during an opt-in prompt.
// Returns true if the key was consumed.
func (e *SpotlightEngine) HandleKey(key string) (tea.Cmd, bool) {
	if e.state != stateOptInPrompt {
		return nil, false
	}
	switch key {
	case "1", "2", "3":
		choice := int(key[0] - '0')
		cand := e.pending
		return func() tea.Msg { return OptInChosenMsg{Candidate: cand, Choice: choice} }, true
	case "4":
		cand := e.pending
		return func() tea.Msg { return OptInTimeoutMsg{Candidate: cand} }, true
	}
	return nil, false
}

// Force pushes a candidate to the head of the queue with a chosen spotlight
// type. The moderator stream uses this to inject a moment ahead of the
// round-robin.
func (e *SpotlightEngine) Force(c Candidate, t SpotlightType) {
	e.override = append([]Candidate{c}, e.override...)
	e.forcedType = &t
}

// State returns a short label for debugging only.
func (e *SpotlightEngine) State() string { return e.state.String() }

// Current returns the active spotlight, or nil when not presenting.
func (e *SpotlightEngine) Current() *Spotlight {
	if e.state != statePresenting {
		return nil
	}
	return e.current
}

// RenderPrompt returns the compact opt-in block shown below the input bar.
// It returns an empty string when no prompt is active.
func (e *SpotlightEngine) RenderPrompt(width int) string {
	if e.state != stateOptInPrompt {
		return ""
	}
	remaining := e.deadline.Sub(e.now())
	if remaining < 0 {
		remaining = 0
	}
	return renderOptInPrompt(e.pending, remaining, width)
}

// ---------------------------------------------------------------------------
// Internal flow
// ---------------------------------------------------------------------------

func (e *SpotlightEngine) tickCmd() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return spotlightTickMsg(t) })
}

func (e *SpotlightEngine) startQueueCmd() tea.Cmd {
	cand, ok := e.pull()
	if !ok {
		e.state = stateIdle
		return nil
	}
	e.state = stateOptInPrompt
	e.pending = cand
	e.deadline = e.now().Add(optInWindow)
	deadline := e.deadline
	return tea.Batch(
		func() tea.Msg { return SpotlightQueuedMsg{Candidate: cand} },
		func() tea.Msg { return OptInPromptedMsg{Candidate: cand, Deadline: deadline} },
	)
}

func (e *SpotlightEngine) pull() (Candidate, bool) {
	if len(e.override) > 0 {
		c := e.override[0]
		e.override = e.override[1:]
		return c, true
	}
	if len(e.roundList) == 0 {
		return Candidate{}, false
	}
	c := e.roundList[e.roundIdx%len(e.roundList)]
	e.roundIdx++
	return c, true
}

func (e *SpotlightEngine) advanceClock(t time.Time) tea.Cmd {
	switch e.state {
	case stateOptInPrompt:
		if !t.Before(e.deadline) {
			cand := e.pending
			return func() tea.Msg { return OptInTimeoutMsg{Candidate: cand} }
		}
	case statePresenting:
		if !t.Before(e.endsAt) {
			ended := *e.current
			e.current = nil
			e.state = stateTransitioning
			e.transitionEndsAt = t.Add(transitionDuration)
			return tea.Batch(
				func() tea.Msg { return SpotlightEndedMsg{Spotlight: ended} },
				func() tea.Msg { return TransitionStartedMsg{} },
			)
		}
	case stateTransitioning:
		if !t.Before(e.transitionEndsAt) {
			return func() tea.Msg { return TransitionCompleteMsg{} }
		}
	}
	return nil
}

func (e *SpotlightEngine) startPresenting(choice int) tea.Cmd {
	t := choiceType(choice)
	if e.forcedType != nil {
		t = *e.forcedType
		e.forcedType = nil
	}
	sp := spotlightFor(e.pending, t)
	dur := defaultDuration(t)
	e.state = statePresenting
	e.current = &sp
	e.endsAt = e.now().Add(dur)
	return func() tea.Msg { return SpotlightStartedMsg{Spotlight: sp} }
}

func (e *SpotlightEngine) beginTransition() tea.Cmd {
	e.state = stateTransitioning
	e.transitionEndsAt = e.now().Add(transitionDuration)
	return func() tea.Msg { return TransitionStartedMsg{} }
}

// choiceType maps an opt-in choice to a spotlight type. 1=repo, 2=topic,
// 3=game. Skip (4) is handled before this is called.
func choiceType(choice int) SpotlightType {
	switch choice {
	case 1:
		return SpotRepo
	case 2:
		return SpotTopic
	case 3:
		return SpotGame
	}
	return SpotShoutout
}

// spotlightFor builds a Spotlight from a candidate. The existing Spotlight
// struct in screens_spotlight.go expects Project/Author/Description/Highlights;
// we fill those from candidate Nick and Bio plus type-specific copy.
func spotlightFor(c Candidate, t SpotlightType) Spotlight {
	nick := c.Nick
	project := fmt.Sprintf("%s · %s", nick, t.String())
	return Spotlight{
		Project:     project,
		Author:      nick,
		Description: c.Bio,
		Highlights:  highlightsFor(t),
	}
}

func highlightsFor(t SpotlightType) []string {
	switch t {
	case SpotRepo:
		return []string{
			"share the repo link in chat",
			"call out what you want eyes on",
			"take questions while you have the floor",
		}
	case SpotTopic:
		return []string{
			"name the topic in one line",
			"open with a take, not a question",
			"yield the floor when the room is hot",
		}
	case SpotGame:
		return []string{
			"pick a game, start the blitz",
			"score is informal, vibes are not",
			"two minutes per round, then rotate",
		}
	case SpotWelcome:
		return []string{"say hi", "say what you build", "say what you want from the room"}
	case SpotHelp:
		return []string{"state the problem", "show what you tried", "drop the repro"}
	case SpotShoutout:
		return []string{"name the person or project", "say why it matters", "keep it short"}
	}
	return nil
}
