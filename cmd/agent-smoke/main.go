// agent-smoke is a one-shot CLI that fires a /agent prompt through
// the real Claude backend (via CLIProxyAPI or Anthropic direct),
// using the same agent.Claude code path the daemon uses.
//
// Usage:
//   agent-smoke --base-url http://127.0.0.1:8317 \
//               --api-key sk-helm-cliproxy-2026 \
//               --model claude-sonnet-4-6 \
//               --workspace ./internal/dispatch \
//               --prompt "read parse.go and tell me what verbs the dispatcher recognizes"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/agent/tools"
)

func main() {
	baseURL := flag.String("base-url", "http://127.0.0.1:8317", "Anthropic-compatible base URL")
	apiKey := flag.String("api-key", os.Getenv("ANTHROPIC_API_KEY"), "API key")
	model := flag.String("model", "claude-sonnet-4-6", "model id")
	workspace := flag.String("workspace", ".", "workspace root (file tools operate here)")
	prompt := flag.String("prompt", "", "the user prompt")
	flag.Parse()
	if *prompt == "" {
		fmt.Fprintln(os.Stderr, "--prompt is required")
		os.Exit(1)
	}
	if *apiKey == "" {
		fmt.Fprintln(os.Stderr, "--api-key or ANTHROPIC_API_KEY is required")
		os.Exit(1)
	}

	// Bind tools to the supplied workspace path. We do not have a
	// real sandbox here (the smoke runs out-of-process), so the exec
	// tools (run, diff) are omitted; the file tools cover the
	// "agent can read your code" demo.
	toolset := tools.DefaultSet(nil, *workspace)
	// Drop nil-sandbox tools so the model does not try to call them.
	usable := agent.Toolset{}
	for _, t := range toolset {
		switch t.Name() {
		case "read_file", "write_file", "list_dir":
			usable = append(usable, t)
		}
	}

	c := agent.NewClaude(*apiKey, usable,
		agent.WithClaudeBaseURL(*baseURL),
		agent.WithClaudeModel(*model),
	)

	fmt.Printf("→ prompt: %s\n", strings.TrimSpace(*prompt))
	fmt.Printf("→ model:  %s\n", *model)
	fmt.Printf("→ url:    %s\n", *baseURL)
	fmt.Printf("→ tools:  %d available\n\n", len(usable))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	resp, err := c.Step(ctx, *prompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	for _, tc := range resp.ToolCalls {
		mark := "·"
		if tc.IsError {
			mark = "!"
		}
		fmt.Printf("[tool %s] %s args=%v\n", mark, tc.Name, tc.Args)
		preview := tc.Result
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		fmt.Printf("          %s\n", strings.ReplaceAll(preview, "\n", "\n          "))
	}
	if resp.Text != "" {
		fmt.Println()
		fmt.Println("agent:")
		fmt.Println(resp.Text)
	}
}
