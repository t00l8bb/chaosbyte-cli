package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/dispatch"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/room"
	"github.com/bchayka/gitstatus/internal/sandbox"
	"github.com/bchayka/gitstatus/internal/sandbox/mock"
	"github.com/bchayka/gitstatus/internal/ui"
)

// TestAgentRespondsViaStub fires /agent through the dispatcher with a
// stub backend and asserts the AgentSaid event lands carrying the
// stub's echo of the prompt.
func TestAgentRespondsViaStub(t *testing.T) {
	rt := mock.New()
	defer rt.Close()
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	defer orch.Close(context.Background())

	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test").
		WithAgentFactory(func(_ identity.Principal) (agent.Agent, error) {
			return agent.NewStub(""), nil
		})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = d.Start(ctx)
	defer d.Stop()

	_, obs := b.Subscribe()
	actor := events.Actor{ID: "pk:test", DisplayName: "@d", Kind: "human", SessionID: uuid.New()}
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/agent help me refactor events/clock.go", ui.ChatNormal))

	got := drainUntil(t, obs, 3*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.AgentSaid); ok {
				return true
			}
		}
		return false
	})

	var said *events.AgentSaid
	for _, e := range got {
		if s, ok := e.(*events.AgentSaid); ok {
			said = s
			break
		}
	}
	if said == nil {
		t.Fatal("missing AgentSaid")
	}
	if !strings.Contains(said.Text, "help me refactor") {
		t.Errorf("agent reply did not echo prompt; got %q", said.Text)
	}
}

// TestAgentMissingBackend confirms /agent surfaces a clear error
// when no agent factory is configured.
func TestAgentMissingBackend(t *testing.T) {
	rt := mock.New()
	defer rt.Close()
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	defer orch.Close(context.Background())

	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	// No WithAgentFactory call.
	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = d.Start(ctx)
	defer d.Stop()

	_, obs := b.Subscribe()
	actor := events.Actor{ID: "pk:test", DisplayName: "@d", Kind: "human", SessionID: uuid.New()}
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/agent hi", ui.ChatNormal))

	got := drainUntil(t, obs, 3*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if c, ok := e.(*events.SandboxCommandCompleted); ok && c.Error != "" {
				return true
			}
		}
		return false
	})
	found := false
	for _, e := range got {
		if c, ok := e.(*events.SandboxCommandCompleted); ok && strings.Contains(c.Error, "no agent backend") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected no-agent error; got %v", got)
	}
}

// TestAgentParseRequiresPrompt covers parse-level validation.
func TestAgentParseRequiresPrompt(t *testing.T) {
	if _, err := dispatch.Parse("/agent"); err == nil {
		t.Error("/agent without prompt should error")
	}
	if _, err := dispatch.Parse("/agent   "); err == nil {
		t.Error("/agent with whitespace-only prompt should error")
	}
	got, err := dispatch.Parse("/agent please refactor events/clock.go")
	if err != nil {
		t.Fatal(err)
	}
	if got.Verb != dispatch.VerbAgent {
		t.Errorf("verb = %q, want agent", got.Verb)
	}
	if got.Raw != "please refactor events/clock.go" {
		t.Errorf("raw = %q", got.Raw)
	}
}
