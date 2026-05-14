package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultClaudeModel is the model we hit when no override is set.
// claude-sonnet-4-5 is a good speed/quality tradeoff for an
// interactive tool-using agent and is what CLIProxyAPI exposes by
// default when Claude Max OAuth is the backing auth.
const DefaultClaudeModel = "claude-sonnet-4-5"

// DefaultClaudeBaseURL targets Anthropic directly. Override with
// WithClaudeBaseURL to point at a local proxy like CLIProxyAPI
// (typically http://127.0.0.1:8317).
const DefaultClaudeBaseURL = "https://api.anthropic.com"

// claudeAPIVersion is the Anthropic API version header value we send.
const claudeAPIVersion = "2023-06-01"

// maxToolUseTurns caps the multi-turn tool loop so a runaway model
// cannot churn forever. Each round is one API call + tool execution.
const maxToolUseTurns = 16

// httpClient is the interface the Claude agent uses to make HTTP
// requests. Pulled out as an interface so tests substitute a fake.
type httpClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Claude is the Anthropic-backed Agent. One per session. Holds a
// fixed Toolset bound to the actor's sandbox + workspace.
type Claude struct {
	apiKey  string
	baseURL string
	model   string
	system  string
	tools   Toolset
	client  httpClient
	timeout time.Duration
}

// ClaudeOption tunes the agent at construction time.
type ClaudeOption func(*Claude)

// WithClaudeModel overrides the model identifier (default: DefaultClaudeModel).
func WithClaudeModel(model string) ClaudeOption {
	return func(c *Claude) {
		if model != "" {
			c.model = model
		}
	}
}

// WithClaudeSystemPrompt sets the system prompt the agent runs under.
func WithClaudeSystemPrompt(system string) ClaudeOption {
	return func(c *Claude) {
		c.system = system
	}
}

// WithClaudeHTTPClient swaps the HTTP client used for tests.
func WithClaudeHTTPClient(hc httpClient) ClaudeOption {
	return func(c *Claude) {
		if hc != nil {
			c.client = hc
		}
	}
}

// WithClaudeBaseURL overrides the API base URL (default:
// DefaultClaudeBaseURL). Used to point at a local proxy like
// CLIProxyAPI. The supplied URL should NOT include the trailing
// /v1/messages path; that is appended automatically.
func WithClaudeBaseURL(base string) ClaudeOption {
	return func(c *Claude) {
		if base != "" {
			c.baseURL = strings.TrimRight(base, "/")
		}
	}
}

// NewClaude returns a Claude-backed Agent. apiKey is required.
// tools is the bound Toolset the agent may invoke. Both apiKey and
// tools may be empty (apiKey returns an Agent that always errors;
// tools means no tool_use loop, just plain text Q&A).
func NewClaude(apiKey string, tools Toolset, opts ...ClaudeOption) *Claude {
	c := &Claude{
		apiKey:  apiKey,
		baseURL: DefaultClaudeBaseURL,
		model:   DefaultClaudeModel,
		tools:   tools,
		client:  &http.Client{Timeout: 90 * time.Second},
		timeout: 90 * time.Second,
		system:  defaultSystemPrompt,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Claude) Kind() string { return "claude" }

// Step runs one full conversation turn. It sends prompt + tools,
// then loops: if the response includes tool_use blocks, execute
// each tool and send the results back; otherwise collect text and
// return.
func (c *Claude) Step(ctx context.Context, prompt string) (Response, error) {
	if c.apiKey == "" {
		return Response{}, fmt.Errorf("claude: ANTHROPIC_API_KEY not set")
	}

	messages := []claudeMessage{
		{Role: "user", Content: []claudeBlock{{Type: "text", Text: prompt}}},
	}

	var toolCalls []ToolCallLog
	var finalText strings.Builder

	for turn := 0; turn < maxToolUseTurns; turn++ {
		resp, err := c.callAPI(ctx, messages)
		if err != nil {
			return Response{ToolCalls: toolCalls}, fmt.Errorf("claude: api: %w", err)
		}

		// Always remember the assistant's reply so the next turn has
		// the full conversation.
		messages = append(messages, claudeMessage{Role: "assistant", Content: resp.Content})

		// Any tool_use blocks must be executed before we send the
		// next user message.
		var toolResults []claudeBlock
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					if finalText.Len() > 0 {
						finalText.WriteByte('\n')
					}
					finalText.WriteString(block.Text)
				}
			case "tool_use":
				args := map[string]any{}
				if block.Input != nil {
					_ = json.Unmarshal(block.Input, &args)
				}
				log := c.runTool(ctx, block.ID, block.Name, args, &toolResults)
				toolCalls = append(toolCalls, log)
			}
		}

		if resp.StopReason != "tool_use" {
			break
		}
		// Feed the tool results back as a user turn so Claude can
		// continue.
		messages = append(messages, claudeMessage{Role: "user", Content: toolResults})
	}

	return Response{Text: finalText.String(), ToolCalls: toolCalls}, nil
}

// runTool executes the named tool and appends a tool_result block
// to results. Returns the structured log entry for the audit trail.
func (c *Claude) runTool(ctx context.Context, useID, name string, args map[string]any, results *[]claudeBlock) ToolCallLog {
	log := ToolCallLog{Name: name, Args: args}
	tool, ok := c.tools.Lookup(name)
	if !ok {
		log.Result = fmt.Sprintf("unknown tool %q", name)
		log.IsError = true
		*results = append(*results, claudeBlock{
			Type:        "tool_result",
			ToolUseID:   useID,
			Content:     log.Result,
			IsError:     true,
		})
		return log
	}
	out, err := tool.Run(ctx, args)
	if err != nil {
		log.Result = err.Error()
		log.IsError = true
	} else {
		log.Result = out.Content
		log.IsError = out.IsError
	}
	*results = append(*results, claudeBlock{
		Type:        "tool_result",
		ToolUseID:   useID,
		Content:     log.Result,
		IsError:     log.IsError,
	})
	return log
}

// callAPI is one HTTP round-trip to /v1/messages.
func (c *Claude) callAPI(ctx context.Context, messages []claudeMessage) (*claudeResponse, error) {
	body := claudeRequest{
		Model:     c.model,
		MaxTokens: 4096,
		System:    c.system,
		Messages:  messages,
	}
	for _, t := range c.tools {
		body.Tools = append(body.Tools, claudeToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.JSONSchema(),
		})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", claudeAPIVersion)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(bodyBytes))
	}
	var out claudeResponse
	if err := json.Unmarshal(bodyBytes, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w (body: %s)", err, string(bodyBytes))
	}
	return &out, nil
}

// --- wire types ---

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []claudeMessage `json:"messages"`
	Tools     []claudeToolDef `json:"tools,omitempty"`
}

type claudeMessage struct {
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type claudeToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type claudeResponse struct {
	ID         string        `json:"id"`
	Content    []claudeBlock `json:"content"`
	StopReason string        `json:"stop_reason"`
	Model      string        `json:"model"`
}

// defaultSystemPrompt is the prelude the agent runs under. Tuned for
// "you are a teammate in a chatroom, you work on the user's
// workspace through tools, you propose changes you don't auto-merge."
const defaultSystemPrompt = `You are an agent inside Monobyte, a collaborative coding chatroom. You have access to a sandboxed workspace and a small set of tools. When the user asks you to do something:

- Use tools to read, write, and run inside the workspace. Don't ask permission for routine reads.
- Keep replies short. The user can see your tool calls; you do not need to narrate.
- Never claim to have done something you didn't actually do via a tool.
- If a task is destructive (deleting code, running mass replacements, running untested commands), describe the plan and wait for confirmation in the next turn.
- The workspace is the user's working repo. Treat it with care.`

var _ Agent = (*Claude)(nil)
