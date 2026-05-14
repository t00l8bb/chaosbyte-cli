// Package agent is the room-side AI participant. Each session that
// opts in gets an Agent bound to its sandbox and worktree. The agent
// can be addressed via /agent <prompt> in chat; it consults the
// configured backend (Claude today, others later), invokes Tools to
// read or write the workspace and run commands, and surfaces every
// step back to the room as typed events the lobby renders.
//
// The Agent interface is intentionally minimal: callers feed it a
// prompt and get back a Response. Streaming and multi-turn memory
// are follow-up enhancements; today every Step is independent and
// returns once the backend stops calling tools.
package agent

import (
	"context"
	"errors"
)

// Agent is the room-side AI participant for one session.
type Agent interface {
	// Step processes one prompt from the user and returns the
	// resulting response. Implementations may invoke Tools an
	// arbitrary number of times before returning; each tool call
	// shows up in Response.ToolCalls.
	Step(ctx context.Context, prompt string) (Response, error)

	// Kind identifies the backend ("stub", "claude") for logs and
	// metrics.
	Kind() string
}

// Response is the agent's final state after one Step. Text is the
// user-facing reply. ToolCalls is the audit trail of every tool the
// agent called during this step, in order.
type Response struct {
	Text      string
	ToolCalls []ToolCallLog
}

// ToolCallLog is one tool invocation, captured for chat-side
// rendering. Args is the JSON-decoded argument map the agent passed;
// Result is the text the tool produced; IsError flags failures.
type ToolCallLog struct {
	Name    string
	Args    map[string]any
	Result  string
	IsError bool
}

// ErrNoAgent is returned by Manager.Get when no agent has been
// constructed for the supplied session id.
var ErrNoAgent = errors.New("agent: no agent for session")
