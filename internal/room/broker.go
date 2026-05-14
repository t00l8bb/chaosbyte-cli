// Package room is the shared multi-user state at the team level. Today
// it owns one channel (#lobby) and a single mod that fires for the
// whole room; per-channel topic/online lists stay on the lobby Screen
// for now.
//
// Phase 1 promoted the previous {Channel, Message} struct to the typed
// events.Event interface. Every publish goes through HLC stamping, an
// optional capability check, and broadcast to typed subscribers. The
// in-memory message log is materialized from ChatPosted events at
// publish time; Phase 1 commit three swaps the in-memory store for a
// SQLite-backed Store.
package room

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/capability"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/mod"
	"github.com/bchayka/gitstatus/internal/store"
	"github.com/bchayka/gitstatus/internal/ui"
)

// Channel is the shared chat scrollback for a room channel.
type Channel struct {
	Name string
}

// SubscriberID identifies a subscriber so it can be removed via
// Unsubscribe. Returned by Subscribe alongside the receive channel.
type SubscriberID uuid.UUID

// subscription bundles the receive channel with its identifier.
type subscription struct {
	id SubscriberID
	ch chan events.Event
}

// Broker is the shared room state. It's safe for concurrent use;
// subscribers each get their own buffered channel and receive every
// event the broker publishes regardless of topic.
type Broker struct {
	mu       sync.Mutex
	clock    *events.Clock
	verifier *capability.Issuer
	store    store.Store
	messages map[string][]ui.ChatMessage
	subs     []subscription
	mod      *mod.Mod
	stop     chan struct{}

	// publish timestamps within the last activityWindow seconds. Drives
	// Tier() so every screen can read the room's energy level without
	// computing its own.
	pubTimes []time.Time

	// roomID is the canonical scope for events the broker publishes.
	// Matches the team slug (e.g., "vibespace").
	roomID string

	// modActor is the canonical Actor used by mod-originated events.
	modActor events.Actor

	// presence tracks who is in the room right now, keyed by Actor.ID.
	// Updated as PresenceJoined / PresenceLeft events flow through
	// PublishEvent. Read by callers via Presence().
	presence map[string]events.Actor

	// servings tracks active dev-server listens, keyed by Actor.ID.
	// Updated as SandboxServing / SandboxServingGone flow through.
	// Read by callers via Servings().
	servings map[string]Serving
}

// Serving is one active dev-server listen as seen by the room. The
// contributor strip in monobyte-osx renders one chip per Serving.
type Serving struct {
	Actor  events.Actor
	Label  string
	Port   int
	Scheme string
	Since  time.Time
}

// activityWindow is the lookback the tier classifier uses on publish times.
const activityWindow = 10 * time.Second

// New starts a broker scoped to a single room. The roomID identifies
// the scope on every event the broker publishes. clock is optional; a
// fresh clock is created if nil. verifier is optional; when nil the
// broker skips capability checks (Phase 1 behavior). When set, every
// PublishEvent with a non-nil CapabilityProof is verified before fan-out.
// st is optional; when nil the broker keeps state in memory only and
// nothing survives restart. When set, every PublishEvent persists via
// st.AppendEvent before fan-out.
func New(roomID string, clock *events.Clock, verifier *capability.Issuer, st store.Store) *Broker {
	if clock == nil {
		clock = events.NewClock()
	}
	// If the store has prior events, advance the clock past the
	// highest persisted timestamp so the next stamp does not collide.
	if st != nil {
		if latest, err := st.LatestHLC(context.Background(), roomID); err == nil && !latest.IsZero() {
			clock.Update(latest)
		}
	}
	b := &Broker{
		clock:    clock,
		verifier: verifier,
		store:    st,
		messages: map[string][]ui.ChatMessage{},
		mod:      mod.New(),
		stop:     make(chan struct{}),
		roomID:   roomID,
		modActor: events.Actor{
			ID:          "mod:" + roomID,
			DisplayName: mod.Nick,
			Kind:        "agent",
			SessionID:   uuid.Nil,
		},
		presence: map[string]events.Actor{},
		servings: map[string]Serving{},
	}
	for ch, msgs := range seedMessages() {
		b.messages[ch] = msgs
	}
	go b.runMod()
	return b
}

// Channels returns the list of seeded channel names. Stable across the
// broker's lifetime (no dynamic create yet).
func (b *Broker) Channels() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.messages))
	for name := range b.messages {
		out = append(out, name)
	}
	return out
}

// Stop terminates the broker's mod goroutine and closes every
// subscriber channel.
func (b *Broker) Stop() {
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
	b.mu.Lock()
	for _, s := range b.subs {
		close(s.ch)
	}
	b.subs = nil
	b.mu.Unlock()
}

// runMod ticks the moderator at 1Hz and publishes any line it returns.
// One mod per room; per-session mods would duplicate prompts.
func (b *Broker) runMod() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.stop:
			return
		case t := <-ticker.C:
			if line := b.mod.Tick(t); line != "" {
				evt := events.NewChatPosted(b.roomID, b.modActor, "#lobby", line, ui.ChatAction)
				evt.At = t
				_ = b.PublishEvent(evt)
			}
		}
	}
}

// Presence returns the list of actors currently in the room. The
// slice is a copy; callers can iterate without holding the broker
// lock. Order is not guaranteed.
func (b *Broker) Presence() []events.Actor {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]events.Actor, 0, len(b.presence))
	for _, a := range b.presence {
		out = append(out, a)
	}
	return out
}

// Servings returns the currently-active dev-server listens in the
// room. The contributor strip in monobyte-osx renders one chip per
// entry. Order is not guaranteed.
func (b *Broker) Servings() []Serving {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Serving, 0, len(b.servings))
	for _, s := range b.servings {
		out = append(out, s)
	}
	return out
}

// LookupServing returns the active serving for a given actor id, or
// false if no such serving exists. Used by the HTTP proxy to resolve
// /u/<actorID> → 127.0.0.1:<port>.
func (b *Broker) LookupServing(actorID string) (Serving, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.servings[actorID]
	return s, ok
}

// Snapshot returns a copy of the channel's scrollback. Callers can
// iterate without holding the broker lock.
func (b *Broker) Snapshot(channel string) []ui.ChatMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.messages[channel]
	out := make([]ui.ChatMessage, len(src))
	copy(out, src)
	return out
}

// Subscribe returns a SubscriberID and a channel that receives every
// future Event the broker publishes. The subscriber type-switches on
// the concrete event type to route. Send is non-blocking; a slow
// subscriber sees its channel buffer fill and subsequent events
// dropped.
//
// Call Unsubscribe with the returned id when the subscriber's session
// ends. Forgetting to unsubscribe leaks a goroutine reference per
// session.
func (b *Broker) Subscribe() (SubscriberID, <-chan events.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := SubscriberID(uuid.New())
	ch := make(chan events.Event, 64)
	b.subs = append(b.subs, subscription{id: id, ch: ch})
	return id, ch
}

// Unsubscribe removes the subscriber and closes its channel. Idempotent
// for already-removed IDs.
func (b *Broker) Unsubscribe(id SubscriberID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, s := range b.subs {
		if s.id == id {
			close(s.ch)
			b.subs = append(b.subs[:i], b.subs[i+1:]...)
			return
		}
	}
}

// PublishEvent stamps an event with an HLC and fans it out to every
// subscriber. The event's ID is assigned if it was zero. ChatPosted
// events are additionally appended to the in-memory scrollback so
// Snapshot reflects the latest state.
//
// Phase 1 keeps this synchronous: PublishEvent returns when fan-out is
// complete. Future phases may move fan-out behind a writer goroutine
// once the typed bus carries higher-rate topics (presence at 20 Hz).
func (b *Broker) PublishEvent(evt events.Event) error {
	if evt.Timestamp().IsZero() {
		evt.SetStamp(b.clock.Now())
	}
	if evt.EventID() == uuid.Nil {
		evt.SetID(uuid.New())
	}

	// Capability check. Phase 1 events typically carry nil proofs; the
	// verify gate fires only when the proof is present and a verifier
	// is configured. Phase 5 will tighten this to require a proof on
	// every privileged kind.
	if b.verifier != nil && evt.CapabilityProof() != nil {
		ctx := capability.VerifyContext{
			Room:   evt.Room(),
			Action: capability.ActionFromKind(evt.EventKind()),
		}
		if _, err := b.verifier.Verify(evt.CapabilityProof(), ctx); err != nil {
			if !errors.Is(err, capability.ErrNoToken) {
				return err
			}
		}
	}

	// Persist before fan-out so a crash mid-publish never loses the
	// event. Subscribers never see an event that does not exist in
	// the durable log.
	if b.store != nil {
		if err := b.store.AppendEvent(context.Background(), evt); err != nil {
			return err
		}
	}

	// Materialize ChatPosted into the in-memory log so existing
	// Snapshot callers continue to see the latest scrollback.
	if chat, ok := evt.(*events.ChatPosted); ok {
		b.appendChat(chat)
	}

	// Track presence so /who and future presence panes can read it
	// without each subscriber maintaining its own copy.
	switch p := evt.(type) {
	case *events.PresenceJoined:
		b.mu.Lock()
		b.presence[p.Actor.ID] = p.Actor
		b.mu.Unlock()
	case *events.PresenceLeft:
		b.mu.Lock()
		delete(b.presence, p.Actor.ID)
		// Servings die with the session that hosted them.
		delete(b.servings, p.Actor.ID)
		b.mu.Unlock()
	case *events.SandboxServing:
		b.mu.Lock()
		b.servings[p.Actor.ID] = Serving{
			Actor:  p.Actor,
			Label:  p.Label,
			Port:   p.Port,
			Scheme: p.Scheme,
			Since:  time.Now(),
		}
		b.mu.Unlock()
	case *events.SandboxServingGone:
		b.mu.Lock()
		delete(b.servings, p.Actor.ID)
		b.mu.Unlock()
	}

	b.mu.Lock()
	subs := append([]subscription(nil), b.subs...)
	b.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- evt:
		default:
			// drop on full buffer; the subscriber's job to drain
		}
	}
	return nil
}

// Publish is the backward-compatible entry point. It wraps a
// ui.ChatMessage into a ChatPosted event and publishes it. The
// moderator inspects the message and may attach tags before broadcast;
// ChatJoin kinds trigger a follow-up welcome from the mod.
//
// This shim keeps existing lobby code compiling while the typed bus
// rolls out. New code should call PublishEvent directly.
func (b *Broker) Publish(channel string, msg ui.ChatMessage) {
	if msg.Kind == ui.ChatNormal {
		if tag := mod.QuestionTag(msg.Body); tag != nil {
			msg.Tags = append(msg.Tags, ui.ChatTag{
				Kind:   tag.Kind,
				Marker: tag.Marker,
				Reason: tag.Reason,
				BornAt: tag.BornAt,
			})
		}
	}
	actor := events.Actor{
		ID:          "legacy:" + msg.Author,
		DisplayName: msg.Author,
		Kind:        "human",
	}
	evt := events.NewChatPosted(b.roomID, actor, channel, msg.Body, msg.Kind)
	evt.At = msg.At

	// The legacy ChatMessage carried fields the events.ChatPosted does
	// not yet model (Tags). Persist via the legacy in-memory append so
	// callers reading Snapshot still see Tags rendered. The event
	// itself carries Body/Kind/At for the subscribers; the lobby's
	// type-switch will pick up tags from the stored ChatMessage when
	// it materializes from Snapshot.
	b.mu.Lock()
	b.messages[channel] = append(b.messages[channel], msg)
	b.mod.NoteChat(msg.At)
	b.pubTimes = append(b.pubTimes, msg.At)
	b.trimPubTimes(msg.At)
	b.mu.Unlock()
	_ = b.fanout(evt)

	if msg.Kind == ui.ChatJoin && msg.Author != "" {
		welcome := ui.ChatMessage{
			Author: mod.Nick,
			Body:   b.mod.Welcome(msg.Author),
			At:     msg.At,
			Kind:   ui.ChatAction,
		}
		b.Publish(channel, welcome)
	}
}

// appendChat mirrors a ChatPosted into the in-memory scrollback. Holds
// the mu lock for the append and the activity tracking.
func (b *Broker) appendChat(chat *events.ChatPosted) {
	msg := chat.AsChatMessage()
	b.mu.Lock()
	b.messages[chat.Channel] = append(b.messages[chat.Channel], msg)
	b.mod.NoteChat(msg.At)
	b.pubTimes = append(b.pubTimes, msg.At)
	b.trimPubTimes(msg.At)
	b.mu.Unlock()
}

// fanout sends an event to every subscriber without persisting. Used by
// the legacy Publish shim which handled persistence inline above.
func (b *Broker) fanout(evt events.Event) error {
	if evt.Timestamp().IsZero() {
		evt.SetStamp(b.clock.Now())
	}
	if evt.EventID() == uuid.Nil {
		evt.SetID(uuid.New())
	}
	b.mu.Lock()
	subs := append([]subscription(nil), b.subs...)
	b.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- evt:
		default:
		}
	}
	return nil
}

// trimPubTimes drops timestamps older than activityWindow. Caller holds mu.
func (b *Broker) trimPubTimes(now time.Time) {
	cutoff := now.Add(-activityWindow)
	keep := b.pubTimes[:0]
	for _, t := range b.pubTimes {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	b.pubTimes = keep
}

// Tier returns the room's current intensity tier derived from publish
// frequency in the last activityWindow seconds. Screens read this each
// tick to keep the field's energy aligned with the room:
//
//	0 quiet: no activity in window
//	1 reactive: 1-2 events
//	2 eventful: 3-5 events
//	3 hype: 6+ events
//
// Tier 4 (game takeover) is screen-local, not room-wide.
func (b *Broker) Tier() int {
	b.mu.Lock()
	b.trimPubTimes(time.Now())
	n := len(b.pubTimes)
	b.mu.Unlock()
	switch {
	case n >= 6:
		return 3
	case n >= 3:
		return 2
	case n >= 1:
		return 1
	}
	return 0
}
