package lobby

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/config"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/identity"
)

// findChannelMessages returns the chat messages for the named channel,
// or nil if no such channel exists.
func findChannelMessages(s *Screen, name string) []chatMessageSnapshot {
	for _, ch := range s.channels {
		if ch.Name == name {
			out := make([]chatMessageSnapshot, len(ch.Messages))
			for i, m := range ch.Messages {
				out[i] = chatMessageSnapshot{Author: m.Author, Body: m.Body, Kind: int(m.Kind)}
			}
			return out
		}
	}
	return nil
}

type chatMessageSnapshot struct {
	Author string
	Body   string
	Kind   int
}

func sandboxActor() events.Actor {
	return events.Actor{
		ID:          "pk:test",
		DisplayName: "@daniel",
		Kind:        "human",
		SessionID:   uuid.New(),
	}
}

func TestSandboxIssuedRendersInChannel(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, config.DefaultVibespace())
	cmdID := uuid.New()
	actor := sandboxActor()
	evt := events.NewSandboxCommandIssued("vibespace", actor, cmdID, actor.SessionID, "#lobby", []string{"echo", "hello"})
	s.handleRoomEvent(evt)

	msgs := findChannelMessages(s, "#lobby")
	found := false
	for _, m := range msgs {
		if strings.Contains(m.Body, "@daniel ran echo hello") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("issued line not rendered in #lobby; got %v", msgs)
	}
}

func TestSandboxOutputFlushesCompleteLines(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, config.DefaultVibespace())
	cmdID := uuid.New()
	actor := sandboxActor()
	s.handleRoomEvent(events.NewSandboxCommandIssued("vibespace", actor, cmdID, actor.SessionID, "#lobby", []string{"echo", "hello"}))

	// Two chunks: one with a complete line + a partial trailing line,
	// then the rest of the partial line.
	s.handleRoomEvent(events.NewSandboxCommandOutput("vibespace", actor, cmdID, events.StreamStdout, []byte("hello\npart")))
	s.handleRoomEvent(events.NewSandboxCommandOutput("vibespace", actor, cmdID, events.StreamStdout, []byte("ial line\n")))

	msgs := findChannelMessages(s, "#lobby")
	var seen []string
	for _, m := range msgs {
		seen = append(seen, m.Body)
	}
	want := []string{"  hello", "  partial line"}
	for _, w := range want {
		found := false
		for _, b := range seen {
			if b == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in scrollback; got %v", w, seen)
		}
	}
}

func TestSandboxCompletedFlushesPartialAndExit(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, config.DefaultVibespace())
	cmdID := uuid.New()
	actor := sandboxActor()
	s.handleRoomEvent(events.NewSandboxCommandIssued("vibespace", actor, cmdID, actor.SessionID, "#lobby", []string{"sh", "-c", "printf no-newline"}))
	s.handleRoomEvent(events.NewSandboxCommandOutput("vibespace", actor, cmdID, events.StreamStdout, []byte("no-newline")))
	s.handleRoomEvent(events.NewSandboxCommandCompleted("vibespace", actor, cmdID, 0, ""))

	msgs := findChannelMessages(s, "#lobby")
	var seen []string
	for _, m := range msgs {
		seen = append(seen, m.Body)
	}
	wantContains := []string{"no-newline", "exit 0"}
	for _, w := range wantContains {
		found := false
		for _, b := range seen {
			if strings.Contains(b, w) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in scrollback; got %v", w, seen)
		}
	}
	if _, still := s.inflightCommands[cmdID]; still {
		t.Error("inflight entry should be deleted after Completed")
	}
}

func TestSandboxCompletedSurfacesError(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, config.DefaultVibespace())
	cmdID := uuid.New()
	actor := sandboxActor()
	s.handleRoomEvent(events.NewSandboxCommandIssued("vibespace", actor, cmdID, actor.SessionID, "#lobby", []string{"nonexistent"}))
	s.handleRoomEvent(events.NewSandboxCommandCompleted("vibespace", actor, cmdID, -1, "exec: file not found"))

	msgs := findChannelMessages(s, "#lobby")
	var seen []string
	for _, m := range msgs {
		seen = append(seen, m.Body)
	}
	found := false
	for _, b := range seen {
		if strings.Contains(b, "error: exec: file not found") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("error line not surfaced; got %v", seen)
	}
}
