package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// flatten walks the synchronous return tree from a tea.Cmd, returning every
// concrete message it would produce. tea.BatchMsg is an unexported slice of
// tea.Cmd; we use reflection to recurse so tests don't have to depend on
// internal Bubbletea types.
func flatten(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	v := reflect.ValueOf(msg)
	if v.Kind() == reflect.Slice {
		var out []tea.Msg
		for i := 0; i < v.Len(); i++ {
			el := v.Index(i)
			if !el.IsValid() {
				continue
			}
			ifc := el.Interface()
			sub, ok := ifc.(tea.Cmd)
			if !ok {
				continue
			}
			out = append(out, flatten(sub)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func findMsg[T any](msgs []tea.Msg) (T, bool) {
	var zero T
	for _, m := range msgs {
		if v, ok := m.(T); ok {
			return v, true
		}
	}
	return zero, false
}

func newTestEngine(t *testing.T) (*SpotlightEngine, func() time.Time, func(time.Duration)) {
	t.Helper()
	clock := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	eng := NewSpotlightEngine([]Candidate{
		{Nick: "@a", Bio: "first"},
		{Nick: "@b", Bio: "second"},
	})
	eng.now = func() time.Time { return clock }
	advance := func(d time.Duration) { clock = clock.Add(d) }
	return eng, eng.now, advance
}

func TestEngineQueuesAndPrompts(t *testing.T) {
	eng, _, _ := newTestEngine(t)
	msgs := flatten(eng.Init())
	if _, ok := findMsg[SpotlightQueuedMsg](msgs); !ok {
		t.Fatalf("expected SpotlightQueuedMsg in init, got %v", msgs)
	}
	if _, ok := findMsg[OptInPromptedMsg](msgs); !ok {
		t.Fatalf("expected OptInPromptedMsg in init, got %v", msgs)
	}
	if eng.State() != "optInPrompt" {
		t.Fatalf("state after init: want optInPrompt, got %s", eng.State())
	}
}

func TestPromptRendersCountdown(t *testing.T) {
	eng, _, _ := newTestEngine(t)
	_ = flatten(eng.Init())
	out := eng.RenderPrompt(80)
	if out == "" {
		t.Fatal("expected prompt output, got empty")
	}
	if !strings.Contains(out, "15s") && !strings.Contains(out, "14s") {
		t.Errorf("countdown not visible: %q", out)
	}
	for _, want := range []string{"1)", "2)", "3)", "4)", "share a repo", "pick a topic", "start a game blitz", "skip"} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q in:\n%s", want, out)
		}
	}
}

func TestPickAdvancesToPresenting(t *testing.T) {
	eng, _, _ := newTestEngine(t)
	_ = flatten(eng.Init())

	cmd, ok := eng.HandleKey("1")
	if !ok {
		t.Fatal("expected key 1 to be consumed during opt-in")
	}
	chosen := cmd()
	if _, ok := chosen.(OptInChosenMsg); !ok {
		t.Fatalf("expected OptInChosenMsg, got %T", chosen)
	}
	_, follow := eng.Update(chosen)
	startMsgs := flatten(follow)
	if _, ok := findMsg[SpotlightStartedMsg](startMsgs); !ok {
		t.Fatalf("expected SpotlightStartedMsg, got %v", startMsgs)
	}
	if eng.State() != "presenting" {
		t.Fatalf("state: want presenting, got %s", eng.State())
	}
	if eng.Current() == nil {
		t.Fatal("Current() should be non-nil while presenting")
	}
}

func TestSkipPullsNextCandidate(t *testing.T) {
	eng, _, _ := newTestEngine(t)
	initMsgs := flatten(eng.Init())
	first, _ := findMsg[OptInPromptedMsg](initMsgs)

	cmd, ok := eng.HandleKey("4")
	if !ok {
		t.Fatal("expected key 4 to be consumed")
	}
	timeoutMsg := cmd()
	_, follow := eng.Update(timeoutMsg)
	transMsgs := flatten(follow)
	if _, ok := findMsg[TransitionStartedMsg](transMsgs); !ok {
		t.Fatalf("expected TransitionStartedMsg after skip, got %v", transMsgs)
	}
	if eng.State() != "transitioning" {
		t.Fatalf("state: want transitioning, got %s", eng.State())
	}

	// finish transition manually
	_, follow = eng.Update(TransitionCompleteMsg{})
	nextMsgs := flatten(follow)
	next, ok := findMsg[OptInPromptedMsg](nextMsgs)
	if !ok {
		t.Fatalf("expected new OptInPromptedMsg after transition complete, got %v", nextMsgs)
	}
	if next.Candidate == first.Candidate {
		t.Fatalf("expected different candidate after skip, both were %v", next.Candidate)
	}
}

func TestTimeoutWhenDeadlinePasses(t *testing.T) {
	eng, _, advance := newTestEngine(t)
	_ = flatten(eng.Init())

	advance(optInWindow + time.Second)
	// Synthesize a tick at the advanced time
	tickAt := eng.now()
	_, follow := eng.Update(spotlightTickMsg(tickAt))
	msgs := flatten(follow)
	if _, ok := findMsg[OptInTimeoutMsg](msgs); !ok {
		t.Fatalf("expected OptInTimeoutMsg after deadline, got %v", msgs)
	}
}

func TestPresentingEndsAfterDuration(t *testing.T) {
	eng, _, advance := newTestEngine(t)
	_ = flatten(eng.Init())

	cmd, _ := eng.HandleKey("1")
	_, follow := eng.Update(cmd())
	_ = flatten(follow)

	advance(defaultDuration(SpotRepo) + time.Second)
	_, follow = eng.Update(spotlightTickMsg(eng.now()))
	msgs := flatten(follow)
	if _, ok := findMsg[SpotlightEndedMsg](msgs); !ok {
		t.Fatalf("expected SpotlightEndedMsg, got %v", msgs)
	}
	if eng.State() != "transitioning" {
		t.Fatalf("state: want transitioning, got %s", eng.State())
	}
}

func TestForcePrependsOverride(t *testing.T) {
	eng, _, _ := newTestEngine(t)
	forced := Candidate{Nick: "@mod-pick", Bio: "moderator override"}
	eng.Force(forced, SpotShoutout)

	msgs := flatten(eng.Init())
	q, ok := findMsg[SpotlightQueuedMsg](msgs)
	if !ok {
		t.Fatalf("expected queued msg, got %v", msgs)
	}
	if q.Candidate != forced {
		t.Fatalf("expected forced candidate first, got %v", q.Candidate)
	}

	cmd, _ := eng.HandleKey("1") // choice doesn't matter, force wins
	_, follow := eng.Update(cmd())
	startMsgs := flatten(follow)
	s, ok := findMsg[SpotlightStartedMsg](startMsgs)
	if !ok {
		t.Fatalf("expected SpotlightStartedMsg, got %v", startMsgs)
	}
	if !strings.Contains(s.Spotlight.Project, "shoutout") {
		t.Errorf("expected forced type shoutout in project label, got %q", s.Spotlight.Project)
	}
}
