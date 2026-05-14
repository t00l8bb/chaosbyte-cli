package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/sandbox"
)

// maxExecOutputBytes caps the captured stdout+stderr for a single
// agent-invoked command so a chatty command does not blow the
// conversation context.
const maxExecOutputBytes = 16 * 1024

// runTool runs an arbitrary command inside the actor's sandbox and
// returns its combined stdout+stderr (truncated) plus exit code.
type runTool struct {
	sb sandbox.Sandbox
}

func (t *runTool) Name() string { return "run" }
func (t *runTool) Description() string {
	return "Run a command inside the workspace sandbox. Returns combined stdout+stderr (truncated to 16 KB) and the exit code."
}
func (t *runTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{
  "type":"object",
  "properties":{
    "argv":{
      "type":"array",
      "items":{"type":"string"},
      "description":"Command and arguments. First element is the binary; subsequent elements are passed as args."
    }
  },
  "required":["argv"]
}`)
}
func (t *runTool) Run(ctx context.Context, args map[string]any) (agent.ToolResult, error) {
	if t.sb == nil {
		return agent.ToolResult{Content: "run tool: no sandbox bound", IsError: true}, nil
	}
	raw, ok := args["argv"].([]any)
	if !ok || len(raw) == 0 {
		return agent.ToolResult{Content: "missing 'argv' argument (array of strings)", IsError: true}, nil
	}
	argv := make([]string, len(raw))
	for i, v := range raw {
		s, ok := v.(string)
		if !ok {
			return agent.ToolResult{Content: fmt.Sprintf("argv[%d] is not a string", i), IsError: true}, nil
		}
		argv[i] = s
	}
	proc, err := t.sb.Exec(ctx, sandbox.Command{Path: argv[0], Args: argv[1:]})
	if err != nil {
		return agent.ToolResult{Content: err.Error(), IsError: true}, nil
	}
	combined := &boundedBuffer{cap: maxExecOutputBytes}
	go func() { _, _ = io.Copy(combined, proc.Stdout()) }()
	go func() { _, _ = io.Copy(combined, proc.Stderr()) }()
	exit, _ := proc.Wait(ctx)
	body := combined.String()
	tag := fmt.Sprintf("\n[exit %d]", exit)
	return agent.ToolResult{Content: body + tag, IsError: exit != 0}, nil
}

// diffTool returns the working-tree diff for the actor's workspace.
type diffTool struct {
	sb sandbox.Sandbox
}

func (t *diffTool) Name() string { return "diff" }
func (t *diffTool) Description() string {
	return "Show the current working-tree diff (git diff) in the workspace. Returns unified diff text."
}
func (t *diffTool) JSONSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *diffTool) Run(ctx context.Context, _ map[string]any) (agent.ToolResult, error) {
	if t.sb == nil {
		return agent.ToolResult{Content: "diff tool: no sandbox bound", IsError: true}, nil
	}
	proc, err := t.sb.Exec(ctx, sandbox.Command{Path: "/usr/bin/git", Args: []string{"diff"}})
	if err != nil {
		// /usr/bin/git on Linux is often /usr/local/bin/git; retry via /bin/sh
		proc, err = t.sb.Exec(ctx, sandbox.Command{Path: "/bin/sh", Args: []string{"-c", "git diff"}})
		if err != nil {
			return agent.ToolResult{Content: err.Error(), IsError: true}, nil
		}
	}
	combined := &boundedBuffer{cap: maxExecOutputBytes}
	go func() { _, _ = io.Copy(combined, proc.Stdout()) }()
	go func() { _, _ = io.Copy(combined, proc.Stderr()) }()
	exit, _ := proc.Wait(ctx)
	body := strings.TrimSpace(combined.String())
	if body == "" {
		return agent.ToolResult{Content: "(no changes)"}, nil
	}
	return agent.ToolResult{Content: body, IsError: exit != 0}, nil
}

// boundedBuffer is a thread-safe writer with a max byte cap. Excess
// writes after cap return discarded bytes silently.
type boundedBuffer struct {
	cap  int
	data []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	left := b.cap - len(b.data)
	if left <= 0 {
		return len(p), nil
	}
	if len(p) > left {
		b.data = append(b.data, p[:left]...)
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (b *boundedBuffer) String() string { return string(b.data) }
