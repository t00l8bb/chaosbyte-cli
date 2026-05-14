package events

import (
	"encoding/json"

	"github.com/google/uuid"
)

const (
	kindAgentSaid       = "agent.said"
	kindAgentToolCalled = "agent.tool.called"
)

// AgentSaid is the user-facing reply from a session's agent after
// processing a /agent prompt. One AgentSaid per Step. The room
// renders it as a chat card under the agent's name.
type AgentSaid struct {
	*Header

	StepID uuid.UUID `json:"step_id"`
	Text   string    `json:"text"`
}

type agentSaidPayload struct {
	StepID uuid.UUID `json:"step_id"`
	Text   string    `json:"text"`
}

func (e *AgentSaid) EventKind() string { return kindAgentSaid }

func (e *AgentSaid) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(agentSaidPayload{StepID: e.StepID, Text: e.Text})
}

// NewAgentSaid builds an AgentSaid event.
func NewAgentSaid(room string, actor Actor, stepID uuid.UUID, text string) *AgentSaid {
	return &AgentSaid{Header: NewHeader(room, actor), StepID: stepID, Text: text}
}

// AgentToolCalled is fired each time the agent invokes a Tool during
// a Step. It is purely informational (for chat-side rendering and
// audit); the agent acts on the tool's result before producing its
// AgentSaid reply, so consumers do not need to act on it.
type AgentToolCalled struct {
	*Header

	StepID  uuid.UUID `json:"step_id"`
	Tool    string    `json:"tool"`
	Args    string    `json:"args"`   // JSON-encoded args for compact wire form
	Result  string    `json:"result"`
	IsError bool      `json:"is_error,omitempty"`
}

type agentToolCalledPayload struct {
	StepID  uuid.UUID `json:"step_id"`
	Tool    string    `json:"tool"`
	Args    string    `json:"args"`
	Result  string    `json:"result"`
	IsError bool      `json:"is_error,omitempty"`
}

func (e *AgentToolCalled) EventKind() string { return kindAgentToolCalled }

func (e *AgentToolCalled) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(agentToolCalledPayload{
		StepID:  e.StepID,
		Tool:    e.Tool,
		Args:    e.Args,
		Result:  e.Result,
		IsError: e.IsError,
	})
}

// NewAgentToolCalled builds an AgentToolCalled event.
func NewAgentToolCalled(room string, actor Actor, stepID uuid.UUID, tool, args, result string, isErr bool) *AgentToolCalled {
	return &AgentToolCalled{
		Header:  NewHeader(room, actor),
		StepID:  stepID,
		Tool:    tool,
		Args:    args,
		Result:  result,
		IsError: isErr,
	}
}
