// Package events defines the typed envelope and topic types that flow
// across the vibespace and monobyte event bus. Every event carries a
// Header with identity, room scope, and an HLC timestamp; payload shape
// is topic-specific.
//
// Producer side: the broker (internal/room) calls SetStamp on incoming
// events to assign the HLC, then persists and fans out. Consumer side:
// the lobby (and future monobyte panes) range over the subscriber
// channel and type-switch on the concrete event type.
//
// The interface is closed in the sense that every topic type lives in
// this package. Adding a topic means adding a struct here, defining
// EventKind on it, and adding a decode case in Unmarshal.
package events

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Event is the closed top type for the bus. Concrete topic types embed
// *Header and add their own payload fields. JSON marshaling preserves
// the EventKind discriminator so the wire format is self-describing.
type Event interface {
	// EventKind returns the discriminator string for this topic
	// (e.g., "chat.posted", "presence.joined").
	EventKind() string

	// EventID is the unique identifier for the event.
	EventID() uuid.UUID

	// Room is the scope this event belongs to. For Phase 1, equal to
	// the team slug (e.g., "vibespace").
	Room() string

	// ActorRef returns the light-weight identity reference. The full
	// identity.Principal is resolved by ID at the session layer.
	ActorRef() Actor

	// Timestamp returns the HLC stamp.
	Timestamp() Timestamp

	// CapabilityProof returns the serialized biscuit token associated
	// with this event. nil pre-Phase 5 or in local mode.
	CapabilityProof() []byte

	// SetStamp is called by the broker to assign an HLC if the event
	// was constructed with the zero Timestamp.
	SetStamp(Timestamp)

	// SetID is called by the broker if EventID was zero.
	SetID(uuid.UUID)

	// SetCapabilityProof attaches a serialized biscuit token. Used by
	// session-side code before Publish.
	SetCapabilityProof([]byte)

	// MarshalPayload returns the topic-specific payload as JSON. The
	// envelope assembler in Marshal wraps this with the Header.
	MarshalPayload() (json.RawMessage, error)
}

// Header is embedded by every concrete event type via *Header. It
// carries the cross-cutting metadata the bus, the store, and the
// capability layer all read without needing to unmarshal the payload.
//
// Header carries a light Actor reference rather than the full
// identity.Principal. The full Principal lives in the session state
// and is resolved by ID when needed.
type Header struct {
	ID        uuid.UUID `json:"id"`
	RoomID    string    `json:"room"`
	Actor     Actor     `json:"actor"`
	Stamp     Timestamp `json:"stamp"`
	Proof     []byte    `json:"capability_proof,omitempty"`
}

// The Event-interface methods that come from the Header are implemented
// on *Header. Concrete topic types embed *Header to inherit them.

func (h *Header) EventID() uuid.UUID         { return h.ID }
func (h *Header) Room() string               { return h.RoomID }
func (h *Header) ActorRef() Actor            { return h.Actor }
func (h *Header) Timestamp() Timestamp       { return h.Stamp }
func (h *Header) CapabilityProof() []byte    { return h.Proof }
func (h *Header) SetStamp(t Timestamp)       { h.Stamp = t }
func (h *Header) SetID(id uuid.UUID)         { h.ID = id }
func (h *Header) SetCapabilityProof(p []byte) { h.Proof = p }

// NewHeader constructs a Header with the room and actor pre-filled.
// ID and Stamp are zero; the broker fills them on Publish.
func NewHeader(room string, actor Actor) *Header {
	return &Header{
		RoomID: room,
		Actor:  actor,
	}
}

// Actor is the light-weight identity reference embedded in every event.
// Kind: "human" or "agent". DisplayName is copied at event-time so
// historical events render correctly even after a principal's display
// name changes.
type Actor struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display"`
	Kind        string    `json:"kind"`
	SessionID   uuid.UUID `json:"session"`
}

// envelope is the wire format used by Marshal/Unmarshal.
type envelope struct {
	Kind    string          `json:"kind"`
	ID      uuid.UUID       `json:"id"`
	Room    string          `json:"room"`
	Actor   Actor           `json:"actor"`
	Stamp   Timestamp       `json:"stamp"`
	Proof   []byte          `json:"capability_proof,omitempty"`
	Payload json.RawMessage `json:"payload"`
}

// Marshal serializes an event to JSON in the canonical envelope format.
func Marshal(e Event) ([]byte, error) {
	payload, err := e.MarshalPayload()
	if err != nil {
		return nil, fmt.Errorf("events: marshal payload for %s: %w", e.EventKind(), err)
	}
	env := envelope{
		Kind:    e.EventKind(),
		ID:      e.EventID(),
		Room:    e.Room(),
		Actor:   e.ActorRef(),
		Stamp:   e.Timestamp(),
		Proof:   e.CapabilityProof(),
		Payload: payload,
	}
	return json.Marshal(env)
}

// Unmarshal decodes a JSON envelope into the concrete event type
// indicated by the kind discriminator. Unknown kinds return an Unknown
// event preserving the raw payload so future topic types do not crash
// older readers.
func Unmarshal(data []byte) (Event, error) {
	var env envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("events: unmarshal envelope: %w", err)
	}
	h := &Header{
		ID:     env.ID,
		RoomID: env.Room,
		Actor:  env.Actor,
		Stamp:  env.Stamp,
		Proof:  env.Proof,
	}
	switch env.Kind {
	case kindChatPosted:
		var p chatPostedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal chat.posted payload: %w", err)
		}
		return &ChatPosted{Header: h, Channel: p.Channel, Body: p.Body, MessageKind: p.MessageKind, At: p.At}, nil
	case kindPresenceJoined:
		var p presenceJoinedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal presence.joined payload: %w", err)
		}
		return &PresenceJoined{Header: h, DisplayName: p.DisplayName}, nil
	case kindPresenceLeft:
		var p presenceLeftPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal presence.left payload: %w", err)
		}
		return &PresenceLeft{Header: h, Reason: p.Reason}, nil
	case kindModTagged:
		var p modTaggedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal mod.tagged payload: %w", err)
		}
		return &ModTagged{Header: h, TargetEventID: p.TargetEventID, Marker: p.Marker, Reason: p.Reason}, nil
	case kindSandboxCommandIssued:
		var p sandboxCommandIssuedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal sandbox.command.issued payload: %w", err)
		}
		return &SandboxCommandIssued{Header: h, CommandID: p.CommandID, SessionID: p.SessionID, Argv: p.Argv, WorkingDir: p.WorkingDir, TTY: p.TTY}, nil
	case kindSandboxCommandOutput:
		var p sandboxCommandOutputPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal sandbox.command.output payload: %w", err)
		}
		return &SandboxCommandOutput{Header: h, CommandID: p.CommandID, Stream: p.Stream, Chunk: p.Chunk}, nil
	case kindSandboxCommandCompleted:
		var p sandboxCommandCompletedPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil, fmt.Errorf("events: unmarshal sandbox.command.completed payload: %w", err)
		}
		return &SandboxCommandCompleted{Header: h, CommandID: p.CommandID, ExitCode: p.ExitCode, Error: p.Error}, nil
	default:
		return &Unknown{Header: h, Kind_: env.Kind, RawPayload: env.Payload}, nil
	}
}

// Unknown carries a topic this build does not recognize. Useful when
// the wire format outpaces the binary; older clients can render them
// as "(unsupported event)" rather than crashing.
type Unknown struct {
	*Header
	Kind_      string
	RawPayload json.RawMessage
}

func (u *Unknown) EventKind() string { return u.Kind_ }
func (u *Unknown) MarshalPayload() (json.RawMessage, error) {
	return u.RawPayload, nil
}
