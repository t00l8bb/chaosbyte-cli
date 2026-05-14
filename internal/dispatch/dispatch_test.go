package dispatch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/dispatch"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/room"
	"github.com/bchayka/gitstatus/internal/sandbox"
	"github.com/bchayka/gitstatus/internal/sandbox/mock"
	"github.com/bchayka/gitstatus/internal/ui"
)

// scriptedRuntime wraps mock.Runtime so we can script the next process's
// stdout/stderr/exit before the dispatcher reads them.
type scriptedRuntime struct {
	*mock.Runtime
	stdout string
	exit   int
}

func newScriptedRuntime(stdout string, exit int) *scriptedRuntime {
	return &scriptedRuntime{Runtime: mock.New(), stdout: stdout, exit: exit}
}

func (r *scriptedRuntime) Spawn(ctx context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	s, err := r.Runtime.Spawn(ctx, spec)
	if err != nil {
		return nil, err
	}
	return &scriptedSandbox{Sandbox: s, stdout: r.stdout, exit: r.exit}, nil
}

type scriptedSandbox struct {
	sandbox.Sandbox
	stdout string
	exit   int
}

func (s *scriptedSandbox) Exec(ctx context.Context, cmd sandbox.Command) (sandbox.Process, error) {
	p, err := s.Sandbox.Exec(ctx, cmd)
	if err != nil {
		return nil, err
	}
	if mp, ok := p.(*mock.Process); ok {
		mp.SetOutput([]byte(s.stdout), s.exit)
	}
	return p, nil
}

// drainUntil collects events from ch until either the predicate
// returns true or the timeout expires. Returns all events seen.
func drainUntil(t *testing.T, ch <-chan events.Event, timeout time.Duration, done func([]events.Event) bool) []events.Event {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	out := []events.Event{}
	for {
		select {
		case evt, ok := <-ch:
			if !ok {
				return out
			}
			out = append(out, evt)
			if done(out) {
				return out
			}
		case <-deadline.C:
			t.Fatalf("timeout waiting for events; collected %d", len(out))
			return out
		}
	}
}

func TestDispatcherRunsSlashCommand(t *testing.T) {
	rt := newScriptedRuntime("hello\n", 0)
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	// Independent subscriber so the test can observe the events the
	// dispatcher publishes.
	_, obs := b.Subscribe()

	actor := events.Actor{
		ID:          "pk:test",
		DisplayName: "@daniel",
		Kind:        "human",
		SessionID:   uuid.New(),
	}
	chat := events.NewChatPosted("test", actor, "#lobby", "/run echo hello", ui.ChatNormal)
	if err := b.PublishEvent(chat); err != nil {
		t.Fatalf("publish chat: %v", err)
	}

	// Expect, in some order: original ChatPosted, then
	// SandboxCommandIssued, one+ SandboxCommandOutput, and
	// SandboxCommandCompleted.
	got := drainUntil(t, obs, 2*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.SandboxCommandCompleted); ok {
				return true
			}
		}
		return false
	})

	var (
		sawChat, sawIssued, sawOutput, sawCompleted bool
		outputBody                                  strings.Builder
		completed                                   *events.SandboxCommandCompleted
		issued                                      *events.SandboxCommandIssued
	)
	for _, e := range got {
		switch v := e.(type) {
		case *events.ChatPosted:
			sawChat = true
		case *events.SandboxCommandIssued:
			sawIssued = true
			issued = v
		case *events.SandboxCommandOutput:
			sawOutput = true
			outputBody.Write(v.Chunk)
		case *events.SandboxCommandCompleted:
			sawCompleted = true
			completed = v
		}
	}
	if !sawChat {
		t.Error("missing ChatPosted")
	}
	if !sawIssued {
		t.Error("missing SandboxCommandIssued")
	}
	if !sawOutput {
		t.Error("missing SandboxCommandOutput")
	}
	if !sawCompleted {
		t.Error("missing SandboxCommandCompleted")
	}
	if issued != nil && (len(issued.Argv) == 0 || issued.Argv[0] != "echo") {
		t.Errorf("issued.Argv = %v, want [echo hello]", issued.Argv)
	}
	if completed != nil && completed.ExitCode != 0 {
		t.Errorf("completed.ExitCode = %d, want 0", completed.ExitCode)
	}
	if got, want := outputBody.String(), "hello\n"; got != want {
		t.Errorf("stdout chunks = %q, want %q", got, want)
	}
	if issued != nil && completed != nil && issued.CommandID != completed.CommandID {
		t.Errorf("commandID mismatch: issued=%v completed=%v", issued.CommandID, completed.CommandID)
	}
}

func TestDispatcherIgnoresNonCommandChat(t *testing.T) {
	rt := newScriptedRuntime("", 0)
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = d.Start(ctx)
	defer d.Stop()

	_, obs := b.Subscribe()

	actor := events.Actor{ID: "pk:test", DisplayName: "@daniel", Kind: "human", SessionID: uuid.New()}
	chat := events.NewChatPosted("test", actor, "#lobby", "hello world", ui.ChatNormal)
	if err := b.PublishEvent(chat); err != nil {
		t.Fatal(err)
	}

	// Give the dispatcher a beat. We should NOT see any sandbox events.
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case evt := <-obs:
			switch evt.(type) {
			case *events.SandboxCommandIssued, *events.SandboxCommandOutput, *events.SandboxCommandCompleted:
				t.Errorf("plain chat should not dispatch; got %T", evt)
			}
		case <-deadline:
			return
		}
	}
}

func TestDispatcherStopIsIdempotent(t *testing.T) {
	rt := newScriptedRuntime("", 0)
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	b := room.New("test", nil, nil, nil)
	defer b.Stop()
	d := dispatch.New(b, orch, "test")
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.Stop()
	d.Stop() // must not panic or hang
}
