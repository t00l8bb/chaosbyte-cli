package lobby

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/bchayka/gitstatus/internal/config"
	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/ui"
)

// chatOnlyConfig is a Vibespace-style RoomConfig: chat enabled,
// dispatcher disabled. Used in tests that exercise the gating layer.
func chatOnlyConfig() config.RoomConfig {
	return config.RoomConfig{
		Slug: "vibespace-test",
		Brand: config.BrandConfig{Name: "vibespace-test"},
		Theme: config.ThemeConfig{
			Bg: lipgloss.Color("#000000"),
			Fg: lipgloss.Color("#ffffff"),
		},
		Surfaces: config.SurfacesConfig{
			Chat: true, Spotlight: true, Games: true,
			Dispatcher: false,
		},
	}
}

// buildEnabledConfig is a Monobyte-style RoomConfig: dispatcher on.
func buildEnabledConfig() config.RoomConfig {
	c := chatOnlyConfig()
	c.Slug = "monobyte-test"
	c.Surfaces.Dispatcher = true
	return c
}

// TestDispatcherVerbsRefusedInChatRoom proves that /run in a chat-
// only room surfaces a friendly hint without publishing to the
// broker.
func TestDispatcherVerbsRefusedInChatRoom(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, chatOnlyConfig())

	_, _ = s.handleSlash("/run echo hello")

	lobby := findChannel(s, "#lobby")
	if lobby == nil {
		t.Fatal("no #lobby channel")
	}
	found := false
	for _, m := range lobby.Messages {
		if strings.Contains(m.Body, "Monobyte verb") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected 'Monobyte verb' hint in scrollback; got %v", lobby.Messages)
	}
	// And the raw /run text must NOT have been posted as user chat
	// (which is how dispatcher verbs would normally reach the broker).
	for _, m := range lobby.Messages {
		if strings.Contains(m.Body, "/run echo hello") {
			t.Errorf("chat-only room should not publish /run; found %q", m.Body)
		}
	}
}

// TestDispatcherVerbsPostedInBuildRoom proves that /run in a
// dispatcher-enabled room IS posted to the channel as a chat
// message so the dispatcher subscriber picks it up.
func TestDispatcherVerbsPostedInBuildRoom(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, buildEnabledConfig())

	_, _ = s.handleSlash("/run echo hi")

	lobby := findChannel(s, "#lobby")
	if lobby == nil {
		t.Fatal("no #lobby channel")
	}
	found := false
	for _, m := range lobby.Messages {
		if m.Body == "/run echo hi" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("dispatcher-enabled room should publish /run as a chat line; got %v", lobby.Messages)
	}
}

// TestHelpHidesDispatcherVerbsInChatRoom: /help in a chat-only room
// must NOT list the Monobyte verbs.
func TestHelpHidesDispatcherVerbsInChatRoom(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, chatOnlyConfig())
	_, _ = s.cmdHelp()

	lobby := findChannel(s, "#lobby")
	if lobby == nil {
		t.Fatal("no #lobby channel")
	}
	body := joinAllBodies(lobby.Messages)
	for _, hidden := range []string{"/run", "/sh", "/pull", "/scratch", "/serve", "/unserve", "/agent"} {
		if strings.Contains(body, hidden) {
			t.Errorf("/help in chat-only room must not show %q; got %q", hidden, body)
		}
	}
	for _, shown := range []string{"/spotlight", "/blitz", "/themes", "/me", "/who", "/clear"} {
		if !strings.Contains(body, shown) {
			t.Errorf("/help in chat-only room must show %q; got %q", shown, body)
		}
	}
}

// TestHelpShowsDispatcherVerbsInBuildRoom: /help in Monobyte rooms
// shows the full list.
func TestHelpShowsDispatcherVerbsInBuildRoom(t *testing.T) {
	s := New(identity.LocalPrincipal(), nil, buildEnabledConfig())
	_, _ = s.cmdHelp()

	lobby := findChannel(s, "#lobby")
	if lobby == nil {
		t.Fatal("no #lobby channel")
	}
	body := joinAllBodies(lobby.Messages)
	for _, shown := range []string{"/run", "/sh", "/pull", "/scratch", "/serve", "/unserve", "/agent"} {
		if !strings.Contains(body, shown) {
			t.Errorf("/help in Monobyte room must show %q; got %q", shown, body)
		}
	}
}

// TestDefaultMonobyteHasDispatcher confirms the built-in config.
func TestDefaultMonobyteHasDispatcher(t *testing.T) {
	cfg := config.DefaultMonobyte()
	if !cfg.Surfaces.Dispatcher {
		t.Error("DefaultMonobyte should enable Surfaces.Dispatcher")
	}
	if cfg.Slug != "monobyte" {
		t.Errorf("slug = %q, want monobyte", cfg.Slug)
	}
}

// TestDefaultVibespaceHasNoDispatcher confirms the chat-only default.
func TestDefaultVibespaceHasNoDispatcher(t *testing.T) {
	cfg := config.DefaultVibespace()
	if cfg.Surfaces.Dispatcher {
		t.Error("DefaultVibespace must NOT enable Dispatcher (chat-only product)")
	}
}

// helpers used only in this file

func findChannel(s *Screen, name string) *Channel {
	for i := range s.channels {
		if s.channels[i].Name == name {
			return &s.channels[i]
		}
	}
	return nil
}

func joinAllBodies(msgs []ui.ChatMessage) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, m.Body)
	}
	return strings.Join(parts, "\n")
}
