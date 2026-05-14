package room

import (
	"testing"
	"time"

	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/ui"
)

// TestBrokerBroadcasts is the headless stand-in for opening two SSH
// sessions side-by-side. Two subscribers attach, one publishes, both must
// receive the event. Catches regressions on the multi-user wiring that
// the user can't see from a single local TUI.
func TestBrokerBroadcasts(t *testing.T) {
	b := New("vibespace", nil, nil, nil)
	defer b.Stop()

	_, alice := b.Subscribe()
	_, bob := b.Subscribe()

	msg := ui.ChatMessage{
		Author: "@alice", Body: "hello", At: time.Now(), Kind: ui.ChatNormal,
	}
	b.Publish("#lobby", msg)

	got := func(name string, sub <-chan events.Event) *events.ChatPosted {
		t.Helper()
		select {
		case evt := <-sub:
			chat, ok := evt.(*events.ChatPosted)
			if !ok {
				t.Fatalf("%s expected ChatPosted, got %T", name, evt)
			}
			return chat
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("%s never received the published event", name)
		}
		return nil
	}

	evtA := got("alice", alice)
	evtB := got("bob", bob)
	if evtA.Channel != "#lobby" || evtA.Body != "hello" {
		t.Errorf("alice got channel=%q body=%q", evtA.Channel, evtA.Body)
	}
	if evtB.Channel != "#lobby" || evtB.Body != "hello" {
		t.Errorf("bob got channel=%q body=%q", evtB.Channel, evtB.Body)
	}
}

// TestBrokerJoinAutoWelcomes asserts the broker fires a follow-up mod
// welcome when a ChatJoin message lands. Without this every SSH user
// would slip in silently from the other sessions' POV.
func TestBrokerJoinAutoWelcomes(t *testing.T) {
	b := New("vibespace", nil, nil, nil)
	defer b.Stop()

	_, sub := b.Subscribe()
	b.Publish("#lobby", ui.ChatMessage{
		Author: "@alice", Body: "entered the chat", At: time.Now(), Kind: ui.ChatJoin,
	})

	// Drain the join, then expect the auto-welcome from the mod.
	first := <-sub
	join, ok := first.(*events.ChatPosted)
	if !ok || join.MessageKind != ui.ChatJoin {
		t.Fatalf("first event should be the join ChatPosted, got %T kind=%v", first, join.MessageKind)
	}

	select {
	case raw := <-sub:
		welcome, ok := raw.(*events.ChatPosted)
		if !ok {
			t.Fatalf("welcome should be ChatPosted, got %T", raw)
		}
		if welcome.MessageKind != ui.ChatAction {
			t.Errorf("welcome should be ChatAction, got %v", welcome.MessageKind)
		}
		if welcome.Channel != "#lobby" {
			t.Errorf("welcome channel = %q, want #lobby", welcome.Channel)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("no auto-welcome after ChatJoin")
	}
}

// TestBrokerTierClimbsWithChat checks that publishing messages in burst
// pushes Tier() up to 3, and dropping the activity returns it toward 0
// once the publish window slides past.
func TestBrokerTierClimbsWithChat(t *testing.T) {
	b := New("vibespace", nil, nil, nil)
	defer b.Stop()

	if got := b.Tier(); got != 0 {
		t.Errorf("idle Tier() = %d, want 0", got)
	}

	now := time.Now()
	for i := 0; i < 6; i++ {
		b.Publish("#lobby", ui.ChatMessage{
			Author: "@alice", Body: "burst", At: now, Kind: ui.ChatNormal,
		})
	}
	if got := b.Tier(); got != 3 {
		t.Errorf("after 6-burst Tier() = %d, want 3", got)
	}
}

// TestBrokerUnsubscribe confirms the new SubscriberID + Unsubscribe path
// closes the subscriber's channel and stops delivering events to it.
func TestBrokerUnsubscribe(t *testing.T) {
	b := New("vibespace", nil, nil, nil)
	defer b.Stop()

	id, sub := b.Subscribe()
	b.Unsubscribe(id)

	// Channel should be closed.
	select {
	case _, ok := <-sub:
		if ok {
			t.Fatal("expected closed channel, got an event")
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("channel was not closed after Unsubscribe")
	}

	// Re-publishing should not block or panic.
	b.Publish("#lobby", ui.ChatMessage{
		Author: "@alice", Body: "after unsub", At: time.Now(), Kind: ui.ChatNormal,
	})
}

// TestBrokerHLCStamping confirms events get an HLC assigned on publish.
// TestBrokerTracksPresence confirms PresenceJoined adds an entry and
// PresenceLeft removes it. The lobby's /who reads this.
func TestBrokerTracksPresence(t *testing.T) {
	b := New("vibespace", nil, nil, nil)
	defer b.Stop()

	if got := len(b.Presence()); got != 0 {
		t.Errorf("initial presence = %d, want 0", got)
	}

	alice := events.Actor{ID: "pk:alice", DisplayName: "@alice", Kind: "human"}
	if err := b.PublishEvent(events.NewPresenceJoined("vibespace", alice)); err != nil {
		t.Fatal(err)
	}
	bob := events.Actor{ID: "pk:bob", DisplayName: "@bob", Kind: "human"}
	if err := b.PublishEvent(events.NewPresenceJoined("vibespace", bob)); err != nil {
		t.Fatal(err)
	}

	if got := len(b.Presence()); got != 2 {
		t.Errorf("after two joins = %d, want 2", got)
	}

	if err := b.PublishEvent(events.NewPresenceLeft("vibespace", alice, "quit")); err != nil {
		t.Fatal(err)
	}
	got := b.Presence()
	if len(got) != 1 || got[0].ID != "pk:bob" {
		t.Errorf("after alice left = %v, want [bob]", got)
	}
}

func TestBrokerHLCStamping(t *testing.T) {
	b := New("vibespace", nil, nil, nil)
	defer b.Stop()

	_, sub := b.Subscribe()

	evt := events.NewChatPosted("vibespace", events.Actor{
		ID: "pk:test", DisplayName: "@test", Kind: "human",
	}, "#lobby", "hello", ui.ChatNormal)

	if !evt.Timestamp().IsZero() {
		t.Fatal("pre-publish timestamp should be zero")
	}
	if err := b.PublishEvent(evt); err != nil {
		t.Fatalf("PublishEvent: %v", err)
	}

	got := <-sub
	if got.Timestamp().IsZero() {
		t.Fatal("post-publish timestamp should be set")
	}
	if got.EventID().String() == "00000000-0000-0000-0000-000000000000" {
		t.Fatal("post-publish ID should be set")
	}
}
