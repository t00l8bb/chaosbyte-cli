package monobyte

import (
	"context"
	"encoding/json"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// agentStepResultMsg is the Bubbletea message a finished agent step
// produces. The TUI's Update folds it into the scrollback.
type agentStepResultMsg struct {
	text      string
	toolCalls []toolCallView
	err       error
}

// toolCallView is a compact, render-ready record of one tool the
// agent invoked during a Step. Args is the JSON-encoded args; preview
// is a truncated first line of the result.
type toolCallView struct {
	name    string
	args    string
	preview string
	isErr   bool
}

// runAgent kicks off an agent step asynchronously. The returned cmd
// produces an agentStepResultMsg when the round-trip completes.
func (m *Model) runAgent(prompt string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := m.stepCtx()
		defer cancel()
		resp, err := m.cfg.Agent.Step(ctx, prompt)
		out := agentStepResultMsg{err: err, text: resp.Text}
		for _, tc := range resp.ToolCalls {
			argsJSON, _ := json.Marshal(tc.Args)
			out.toolCalls = append(out.toolCalls, toolCallView{
				name:    tc.Name,
				args:    truncate(string(argsJSON), 80),
				preview: truncate(strings.SplitN(strings.TrimSpace(tc.Result), "\n", 2)[0], 200),
				isErr:   tc.IsError,
			})
		}
		return out
	}
}

// handleAgentResult is called from Update when an agent step lands.
func (m *Model) handleAgentResult(r agentStepResultMsg) {
	for _, tc := range r.toolCalls {
		mark := "·"
		if tc.isErr {
			mark = "!"
		}
		m.append(line{
			kind: lineTool,
			text: mark + " " + tc.name + "(" + tc.args + ") → " + tc.preview,
		})
	}
	if r.err != nil && !isCtxCancel(r.err) {
		m.append(line{kind: lineError, text: r.err.Error()})
		return
	}
	if r.text != "" {
		m.append(line{kind: lineAgent, text: r.text})
	} else if len(r.toolCalls) == 0 {
		m.append(line{kind: lineSystem, text: "(no reply)"})
	}
}

func isCtxCancel(err error) bool {
	return err == context.Canceled
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
