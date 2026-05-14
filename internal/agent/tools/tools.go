// Package tools is the concrete set of capabilities an agent can
// invoke during a Step. Every Tool wraps the actor's sandbox or
// worktree so all side effects are confined to the per-session
// fence: the agent cannot read or write outside the workspace, run
// commands outside the sandbox, or reach the network.
//
// Tool name / description / schema follow the Anthropic tool_use
// shape so a Claude backend can hand them straight through; future
// backends conform to the same envelope.
package tools

import (
	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/sandbox"
)

// DefaultSet returns the canonical toolset bound to the supplied
// sandbox + workspace path. workspacePath is the host-side filesystem
// path of the actor's worktree, used for tools that need to read or
// write files directly (without going through the sandbox shell).
//
// The order is significant: backends pass the toolset to the LLM in
// this order, so reproducibility lives here.
func DefaultSet(sb sandbox.Sandbox, workspacePath string) agent.Toolset {
	return agent.Toolset{
		&readFileTool{ws: workspacePath},
		&writeFileTool{ws: workspacePath},
		&listDirTool{ws: workspacePath},
		&runTool{sb: sb},
		&diffTool{sb: sb},
	}
}
