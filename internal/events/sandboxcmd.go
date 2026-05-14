package events

import (
	"encoding/json"

	"github.com/google/uuid"
)

const (
	kindSandboxCommandIssued    = "sandbox.command.issued"
	kindSandboxCommandOutput    = "sandbox.command.output"
	kindSandboxCommandCompleted = "sandbox.command.completed"
)

// SandboxCommandIssued is fired when a chat message is recognized as
// a sandbox-bound command (e.g. `/run cargo test`). It is published
// after the original ChatPosted so consumers can correlate the visible
// chat line with the in-flight execution.
//
// CommandID is the dispatcher-assigned correlation id that ties every
// SandboxCommandOutput and the final SandboxCommandCompleted back to
// this issuance.
type SandboxCommandIssued struct {
	*Header

	CommandID  uuid.UUID `json:"command_id"`
	SessionID  uuid.UUID `json:"session_id"`
	Argv       []string  `json:"argv"`
	WorkingDir string    `json:"working_dir,omitempty"`
	TTY        bool      `json:"tty,omitempty"`
}

type sandboxCommandIssuedPayload struct {
	CommandID  uuid.UUID `json:"command_id"`
	SessionID  uuid.UUID `json:"session_id"`
	Argv       []string  `json:"argv"`
	WorkingDir string    `json:"working_dir,omitempty"`
	TTY        bool      `json:"tty,omitempty"`
}

func (e *SandboxCommandIssued) EventKind() string { return kindSandboxCommandIssued }

func (e *SandboxCommandIssued) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(sandboxCommandIssuedPayload{
		CommandID:  e.CommandID,
		SessionID:  e.SessionID,
		Argv:       e.Argv,
		WorkingDir: e.WorkingDir,
		TTY:        e.TTY,
	})
}

// NewSandboxCommandIssued constructs an event for a parsed slash
// command. The broker assigns ID and Stamp on Publish.
func NewSandboxCommandIssued(room string, actor Actor, commandID, sessionID uuid.UUID, argv []string) *SandboxCommandIssued {
	return &SandboxCommandIssued{
		Header:    NewHeader(room, actor),
		CommandID: commandID,
		SessionID: sessionID,
		Argv:      argv,
	}
}

// Stream names for SandboxCommandOutput.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// SandboxCommandOutput is one chunk of stdout or stderr from a running
// command. Chunks are bounded in size by the dispatcher's reader so
// the broker buffer does not stall.
type SandboxCommandOutput struct {
	*Header

	CommandID uuid.UUID `json:"command_id"`
	Stream    string    `json:"stream"`
	Chunk     []byte    `json:"chunk"`
}

type sandboxCommandOutputPayload struct {
	CommandID uuid.UUID `json:"command_id"`
	Stream    string    `json:"stream"`
	Chunk     []byte    `json:"chunk"`
}

func (e *SandboxCommandOutput) EventKind() string { return kindSandboxCommandOutput }

func (e *SandboxCommandOutput) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(sandboxCommandOutputPayload{
		CommandID: e.CommandID,
		Stream:    e.Stream,
		Chunk:     e.Chunk,
	})
}

// NewSandboxCommandOutput builds an output frame.
func NewSandboxCommandOutput(room string, actor Actor, commandID uuid.UUID, stream string, chunk []byte) *SandboxCommandOutput {
	return &SandboxCommandOutput{
		Header:    NewHeader(room, actor),
		CommandID: commandID,
		Stream:    stream,
		Chunk:     append([]byte(nil), chunk...),
	}
}

// SandboxCommandCompleted fires when the command exits. ExitCode is
// the process exit status; Error is non-empty if the dispatcher could
// not start or supervise the command (sandbox unavailable, parse
// error after publish, etc.).
type SandboxCommandCompleted struct {
	*Header

	CommandID uuid.UUID `json:"command_id"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
}

type sandboxCommandCompletedPayload struct {
	CommandID uuid.UUID `json:"command_id"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
}

func (e *SandboxCommandCompleted) EventKind() string { return kindSandboxCommandCompleted }

func (e *SandboxCommandCompleted) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(sandboxCommandCompletedPayload{
		CommandID: e.CommandID,
		ExitCode:  e.ExitCode,
		Error:     e.Error,
	})
}

// NewSandboxCommandCompleted builds a completion event.
func NewSandboxCommandCompleted(room string, actor Actor, commandID uuid.UUID, exitCode int, errMsg string) *SandboxCommandCompleted {
	return &SandboxCommandCompleted{
		Header:    NewHeader(room, actor),
		CommandID: commandID,
		ExitCode:  exitCode,
		Error:     errMsg,
	}
}
