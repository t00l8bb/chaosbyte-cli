// monobyte is the Claude-Code-style terminal AI for chaosbyte. The
// user types `monobyte` in any terminal and from that point the
// terminal has hidden IDE powers: a chat-driven agent, a sandboxed
// command runner, and a browser canvas that opens when needed.
//
// Input model (v1):
//
//	plain text   → Claude (default; tools shape the terminal)
//	/verb args   → direct dispatcher (power-user shortcut)
//	!cmd args    → raw shell in $PWD (power-user shortcut)
//
// See vault/products/Monobyte CLI v1 spec 2026-05-13.md for the
// full design.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bchayka/gitstatus/internal/agent"
	"github.com/bchayka/gitstatus/internal/agent/tools"
	"github.com/bchayka/gitstatus/internal/app/monobyte"
	"github.com/bchayka/gitstatus/internal/sandbox"
	"github.com/bchayka/gitstatus/internal/sandbox/host"

	tea "github.com/charmbracelet/bubbletea"
)

func main() {
	workspaceFlag := flag.String("workspace", "", "directory the agent operates on (default: current directory)")
	apiKeyFlag := flag.String("api-key", "", "API key (default: $ANTHROPIC_API_KEY or $CLIPROXY_API_KEY)")
	baseURLFlag := flag.String("base-url", "", "API base URL (default: probes http://127.0.0.1:8317, falls back to https://api.anthropic.com)")
	modelFlag := flag.String("model", "", "model id (default: claude-sonnet-4-6)")
	flag.Parse()

	workspace := resolveWorkspace(*workspaceFlag)
	apiKey := resolveAPIKey(*apiKeyFlag)
	baseURL := resolveBaseURL(*baseURLFlag)
	model := *modelFlag
	if model == "" {
		model = agent.DefaultClaudeModel
	}

	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "monobyte: no API key. set $ANTHROPIC_API_KEY (Anthropic direct) or $CLIPROXY_API_KEY (with cliproxyapi running on 127.0.0.1:8317), or pass --api-key.")
		os.Exit(1)
	}

	rt, err := host.New(filepath.Join(monobyteHome(), "sandboxes"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "monobyte: could not initialize sandbox:", err)
		os.Exit(1)
	}
	defer rt.Close()

	// One sandbox for the whole monobyte session, with the user's
	// workspace bind-mounted so the agent's tools (read_file, write_file,
	// run) operate on it directly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sb, err := rt.Spawn(ctx, sandbox.Spec{
		Mounts: []sandbox.Mount{
			{HostPath: workspace, SandboxPath: workspace, ReadOnly: false},
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "monobyte: could not spawn sandbox:", err)
		os.Exit(1)
	}
	defer sb.Destroy(context.Background())

	toolset := tools.DefaultSet(sb, workspace)
	claude := agent.NewClaude(apiKey, toolset,
		agent.WithClaudeBaseURL(baseURL),
		agent.WithClaudeModel(model),
	)

	app := monobyte.New(monobyte.Config{
		Workspace: workspace,
		Sandbox:   sb,
		Agent:     claude,
		Model:     model,
		BaseURL:   baseURL,
	})

	prog := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "monobyte:", err)
		os.Exit(1)
	}
}

// resolveWorkspace returns the workspace path. Precedence:
//
//	--workspace flag → $MONOBYTE_WORKSPACE → $PWD
func resolveWorkspace(flagVal string) string {
	if flagVal != "" {
		abs, err := filepath.Abs(flagVal)
		if err == nil {
			return abs
		}
		return flagVal
	}
	if env := os.Getenv("MONOBYTE_WORKSPACE"); env != "" {
		return env
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

// resolveAPIKey picks the first non-empty: --api-key, ANTHROPIC_API_KEY,
// CLIPROXY_API_KEY.
func resolveAPIKey(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	for _, name := range []string{"ANTHROPIC_API_KEY", "CLIPROXY_API_KEY", "MONOBYTE_API_KEY"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	return ""
}

// resolveBaseURL picks --base-url, $ANTHROPIC_BASE_URL, $MONOBYTE_BASE_URL,
// or probes the local CLIProxyAPI; falls back to api.anthropic.com.
func resolveBaseURL(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	for _, name := range []string{"ANTHROPIC_BASE_URL", "MONOBYTE_BASE_URL"} {
		if v := os.Getenv(name); v != "" {
			return v
		}
	}
	// Probe the canonical local CLIProxyAPI port. 1 second timeout so
	// we do not hang the launch.
	client := &http.Client{Timeout: 1 * time.Second}
	if resp, err := client.Get("http://127.0.0.1:8317/v1/models"); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode < 500 {
			return "http://127.0.0.1:8317"
		}
	}
	return agent.DefaultClaudeBaseURL
}

// monobyteHome returns ~/.monobyte/ (created on demand).
func monobyteHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".monobyte")
}
