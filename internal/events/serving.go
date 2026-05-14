package events

import (
	"encoding/json"

	"github.com/google/uuid"
)

const (
	kindSandboxServing     = "sandbox.serving"
	kindSandboxServingGone = "sandbox.serving.gone"
)

// SandboxServing is fired when a sandboxed dev server starts listening
// on a port. It carries the user id, a human label (e.g. "next-dev",
// "cargo-watch"), the port number, and the URL scheme. The broker
// keeps a live index of active servings keyed by user id; subscribers
// (in particular the monobyte-osx contributor strip) read it to know
// who in the room is serving what.
//
// A user with no active serving has no SandboxServing in flight. To
// stop, the user (or the dispatcher on session end) publishes
// SandboxServingGone with the same user id.
type SandboxServing struct {
	*Header

	UserSessionID uuid.UUID `json:"user_session_id"`
	Label         string    `json:"label"`
	Port          int       `json:"port"`
	Scheme        string    `json:"scheme"`
}

type sandboxServingPayload struct {
	UserSessionID uuid.UUID `json:"user_session_id"`
	Label         string    `json:"label"`
	Port          int       `json:"port"`
	Scheme        string    `json:"scheme"`
}

func (e *SandboxServing) EventKind() string { return kindSandboxServing }

func (e *SandboxServing) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(sandboxServingPayload{
		UserSessionID: e.UserSessionID,
		Label:         e.Label,
		Port:          e.Port,
		Scheme:        e.Scheme,
	})
}

// NewSandboxServing constructs a serving event. The broker assigns
// ID and Stamp on Publish.
func NewSandboxServing(room string, actor Actor, sessionID uuid.UUID, label string, port int, scheme string) *SandboxServing {
	if scheme == "" {
		scheme = "http"
	}
	return &SandboxServing{
		Header:        NewHeader(room, actor),
		UserSessionID: sessionID,
		Label:         label,
		Port:          port,
		Scheme:        scheme,
	}
}

// SandboxServingGone is fired when a serving stops, either because the
// user explicitly stopped it (/unserve), the dev process exited, or
// the session ended (presence left).
type SandboxServingGone struct {
	*Header

	UserSessionID uuid.UUID `json:"user_session_id"`
	Reason        string    `json:"reason,omitempty"`
}

type sandboxServingGonePayload struct {
	UserSessionID uuid.UUID `json:"user_session_id"`
	Reason        string    `json:"reason,omitempty"`
}

func (e *SandboxServingGone) EventKind() string { return kindSandboxServingGone }

func (e *SandboxServingGone) MarshalPayload() (json.RawMessage, error) {
	return json.Marshal(sandboxServingGonePayload{
		UserSessionID: e.UserSessionID,
		Reason:        e.Reason,
	})
}

// NewSandboxServingGone constructs the stop event.
func NewSandboxServingGone(room string, actor Actor, sessionID uuid.UUID, reason string) *SandboxServingGone {
	return &SandboxServingGone{
		Header:        NewHeader(room, actor),
		UserSessionID: sessionID,
		Reason:        reason,
	}
}
