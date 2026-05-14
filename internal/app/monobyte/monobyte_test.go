package monobyte

import (
	"context"
	"strings"
	"testing"

	"github.com/bchayka/gitstatus/internal/agent"
)

// fakeAgent records the prompt it received and returns a canned
// Response. Lets the router test run without a network or a TTY.
type fakeAgent struct {
	lastPrompt string
	reply      string
}

func (f *fakeAgent) Kind() string { return "fake" }
func (f *fakeAgent) Step(_ context.Context, prompt string) (agent.Response, error) {
	f.lastPrompt = prompt
	return agent.Response{Text: f.reply}, nil
}

// TestInputRouterAgentDefault: plain text goes to the agent.
func TestInputRouterAgentDefault(t *testing.T) {
	fa := &fakeAgent{reply: "hi back"}
	m := New(Config{Workspace: "/tmp", Agent: fa}).(*Model)
	cmd := m.handleInput("hi there")
	if cmd == nil {
		t.Fatal("expected agent cmd")
	}
	msg := cmd()
	if _, ok := msg.(agentStepResultMsg); !ok {
		t.Fatalf("router did not produce agent msg; got %T", msg)
	}
	if fa.lastPrompt != "hi there" {
		t.Errorf("agent saw %q, want %q", fa.lastPrompt, "hi there")
	}
}

// TestInputRouterBangIsShell: `!cmd` goes to the shell runner.
func TestInputRouterBangIsShell(t *testing.T) {
	fa := &fakeAgent{}
	m := New(Config{Workspace: "/tmp", Agent: fa}).(*Model)
	cmd := m.handleInput("!echo hello")
	if cmd == nil {
		t.Fatal("expected shell cmd")
	}
	msg := cmd()
	r, ok := msg.(shellResultMsg)
	if !ok {
		t.Fatalf("router did not produce shell msg; got %T", msg)
	}
	if !strings.Contains(r.output, "hello") {
		t.Errorf("shell output = %q, want it to contain 'hello'", r.output)
	}
	if fa.lastPrompt != "" {
		t.Errorf("agent should not have been called for !cmd; saw %q", fa.lastPrompt)
	}
}

// TestInputRouterSlashHelp: /help goes to the dispatcher.
func TestInputRouterSlashHelp(t *testing.T) {
	fa := &fakeAgent{}
	m := New(Config{Workspace: "/tmp", Agent: fa}).(*Model)
	cmd := m.handleInput("/help")
	if cmd == nil {
		t.Fatal("expected dispatcher cmd")
	}
	msg := cmd()
	r, ok := msg.(dispatcherResultMsg)
	if !ok {
		t.Fatalf("router did not produce dispatcher msg; got %T", msg)
	}
	if !strings.Contains(r.text, "monobyte") {
		t.Errorf("/help did not include 'monobyte' in text; got %q", r.text)
	}
	if fa.lastPrompt != "" {
		t.Errorf("agent should not be called for /help")
	}
}

// TestInputRouterEmptyPrefix: lone ! or / is a no-op (no crash).
func TestInputRouterEmptyPrefix(t *testing.T) {
	fa := &fakeAgent{}
	m := New(Config{Workspace: "/tmp", Agent: fa}).(*Model)
	cmd := m.handleInput("!")
	if cmd == nil {
		t.Fatal("expected a cmd even for empty shell")
	}
	msg := cmd()
	if _, ok := msg.(shellResultMsg); !ok {
		t.Errorf("got %T", msg)
	}
}
