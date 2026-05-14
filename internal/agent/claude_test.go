package agent_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bchayka/gitstatus/internal/agent"
)

// fakeHTTP captures each request and replies with a scripted
// sequence of responses. Used to drive the tool_use loop without
// hitting the real Anthropic API.
type fakeHTTP struct {
	responses []string
	calls     int
	gotReqs   []string
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		f.gotReqs = append(f.gotReqs, string(body))
	}
	if f.calls >= len(f.responses) {
		return &http.Response{
			StatusCode: 500,
			Body:       io.NopCloser(strings.NewReader(`{"error":"out of scripted responses"}`)),
		}, nil
	}
	body := f.responses[f.calls]
	f.calls++
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// fakeTool is a deterministic Tool that records every call and
// returns a canned response.
type fakeTool struct {
	name   string
	out    string
	called int
	args   []map[string]any
	isErr  bool
}

func (t *fakeTool) Name() string             { return t.name }
func (t *fakeTool) Description() string      { return "fake" }
func (t *fakeTool) JSONSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *fakeTool) Run(_ context.Context, args map[string]any) (agent.ToolResult, error) {
	t.called++
	t.args = append(t.args, args)
	return agent.ToolResult{Content: t.out, IsError: t.isErr}, nil
}

// TestClaudeTextOnly exercises the simplest path: the model replies
// once with plain text and stop_reason=end_turn.
func TestClaudeTextOnly(t *testing.T) {
	hc := &fakeHTTP{responses: []string{
		`{"id":"msg_1","content":[{"type":"text","text":"hello back"}],"stop_reason":"end_turn"}`,
	}}
	c := agent.NewClaude("test-key", nil, agent.WithClaudeHTTPClient(hc))
	resp, err := c.Step(context.Background(), "hi there")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "hello back" {
		t.Errorf("text = %q", resp.Text)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
	}
	if hc.calls != 1 {
		t.Errorf("calls = %d, want 1", hc.calls)
	}
}

// TestClaudeToolUseLoop exercises the multi-turn case: model
// responds with tool_use, we run the tool, send the result back,
// then model returns plain text.
func TestClaudeToolUseLoop(t *testing.T) {
	tool := &fakeTool{name: "read_file", out: "package main\nfunc main() {}\n"}
	hc := &fakeHTTP{responses: []string{
		// Turn 1: model decides to call read_file.
		`{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"main.go"}}],"stop_reason":"tool_use"}`,
		// Turn 2: model returns final text after seeing the tool result.
		`{"id":"msg_2","content":[{"type":"text","text":"the file looks fine"}],"stop_reason":"end_turn"}`,
	}}
	c := agent.NewClaude("test-key", agent.Toolset{tool}, agent.WithClaudeHTTPClient(hc))
	resp, err := c.Step(context.Background(), "look at main.go")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != "the file looks fine" {
		t.Errorf("text = %q", resp.Text)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "read_file" {
		t.Errorf("tool name = %q", resp.ToolCalls[0].Name)
	}
	if tool.called != 1 {
		t.Errorf("tool called %d times", tool.called)
	}
	// The second API call must include the tool_result block.
	if !strings.Contains(hc.gotReqs[1], "tool_result") {
		t.Errorf("second request did not include tool_result: %s", hc.gotReqs[1])
	}
	if !strings.Contains(hc.gotReqs[1], "package main") {
		t.Errorf("second request did not carry the tool output")
	}
}

// TestClaudeUnknownToolReturnsErrorBlock confirms a malformed model
// reply (asking for a tool we don't have) yields a clean tool_result
// with is_error=true rather than crashing the loop.
func TestClaudeUnknownToolReturnsErrorBlock(t *testing.T) {
	hc := &fakeHTTP{responses: []string{
		`{"id":"msg_1","content":[{"type":"tool_use","id":"toolu_1","name":"nope","input":{}}],"stop_reason":"tool_use"}`,
		`{"id":"msg_2","content":[{"type":"text","text":"sorry, no such tool"}],"stop_reason":"end_turn"}`,
	}}
	c := agent.NewClaude("test-key", nil, agent.WithClaudeHTTPClient(hc))
	resp, err := c.Step(context.Background(), "do something weird")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call entry, got %d", len(resp.ToolCalls))
	}
	if !resp.ToolCalls[0].IsError {
		t.Error("unknown tool log should be IsError")
	}
}

// TestClaudeMissingAPIKey: Step returns an error immediately when no
// key is configured.
func TestClaudeMissingAPIKey(t *testing.T) {
	c := agent.NewClaude("", nil)
	if _, err := c.Step(context.Background(), "hi"); err == nil {
		t.Error("expected error when api key is empty")
	}
}

func TestClaudeKindIsClaude(t *testing.T) {
	c := agent.NewClaude("k", nil)
	if c.Kind() != "claude" {
		t.Errorf("Kind = %q", c.Kind())
	}
}

// TestClaudeBaseURLProxiesToCLIProxyAPI confirms the agent points at
// a custom base URL when configured (CLIProxyAPI mode).
func TestClaudeBaseURLProxiesToCLIProxyAPI(t *testing.T) {
	hc := &capturingHTTP{response: `{"id":"msg_1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`}
	c := agent.NewClaude(
		"sk-helm-cliproxy-2026",
		nil,
		agent.WithClaudeHTTPClient(hc),
		agent.WithClaudeBaseURL("http://127.0.0.1:8317"),
	)
	if _, err := c.Step(context.Background(), "ping"); err != nil {
		t.Fatal(err)
	}
	if hc.gotURL != "http://127.0.0.1:8317/v1/messages" {
		t.Errorf("URL = %q, want CLIProxyAPI base", hc.gotURL)
	}
	if hc.gotHeader("x-api-key") != "sk-helm-cliproxy-2026" {
		t.Errorf("x-api-key = %q", hc.gotHeader("x-api-key"))
	}
	if hc.gotHeader("anthropic-version") == "" {
		t.Error("anthropic-version header missing")
	}
}

// capturingHTTP records the last request and replies with a single
// canned response.
type capturingHTTP struct {
	response   string
	gotURL     string
	gotHeaders http.Header
}

func (h *capturingHTTP) Do(req *http.Request) (*http.Response, error) {
	h.gotURL = req.URL.String()
	h.gotHeaders = req.Header.Clone()
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(h.response)),
		Header:     http.Header{},
	}, nil
}

func (h *capturingHTTP) gotHeader(name string) string {
	return h.gotHeaders.Get(name)
}
