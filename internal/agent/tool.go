package agent

import (
	"context"
	"encoding/json"
)

// Tool is a single capability the agent backend may invoke during a
// Step. Tools wrap operations on the sandbox + worktree: read a
// file, write a file, run a command, get the current diff.
//
// Tools are stateless from the agent's perspective. Each Run call
// receives the JSON-decoded arguments the backend produced; returns
// a Result whose Content becomes the tool's reply in the next round
// of the conversation.
type Tool interface {
	Name() string
	Description() string
	JSONSchema() json.RawMessage
	Run(ctx context.Context, args map[string]any) (ToolResult, error)
}

// ToolResult is the structured response a Tool returns to the
// backend. Content is what the backend sees; IsError flags failures
// so the backend can decide whether to retry or abort.
type ToolResult struct {
	Content string
	IsError bool
}

// Toolset is an ordered list of tools the agent can call. The order
// is preserved when handed to the backend so logs and traces are
// reproducible.
type Toolset []Tool

// Lookup returns the tool by name and a found bool.
func (ts Toolset) Lookup(name string) (Tool, bool) {
	for _, t := range ts {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}
