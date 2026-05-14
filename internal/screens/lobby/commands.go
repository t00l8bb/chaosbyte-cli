package lobby

import (
	"fmt"
	"strings"
	"time"

	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/games"
	"github.com/bchayka/gitstatus/internal/screens"
	"github.com/bchayka/gitstatus/internal/theme"
	"github.com/bchayka/gitstatus/internal/ui"
	tea "github.com/charmbracelet/bubbletea"
)

// command is one entry in the slash-command catalog. handlers live in the
// dispatch switch rather than as function pointers on this struct, which lets
// handlers reference `builtins` without creating a package-level init cycle.
type command struct {
	name string
	desc string
}

// builtins is the canonical list of slash commands. Order here is the order
// shown in autocomplete. Aliases are wired in `aliases` and don't appear here
// to keep the suggestion strip tidy.
var builtins = []command{
	{"/spotlight", "open the current spotlit project"},
	{"/blitz", "thirty seconds where the whole chat dances and we name a winner"},
	{"/themes", "list color themes, or /themes <name> to switch"},
	{"/me", "third-person action"},
	{"/who", "list who is here"},
	{"/clear", "clear scrollback"},
	{"/help", "show all commands"},
	{"/leave", "leave the room"},
	{"/quit", "exit vibespace"},
}

// aliases maps an alternate spelling to its canonical command name. Aliases
// don't show in autocomplete.
var aliases = map[string]string{
	"/exit":  "/quit",
	"/bye":   "/quit",
	"/users": "/who",
	"/?":     "/help",
}

// canonicalName resolves aliases to their primary command name.
func canonicalName(name string) string {
	if a, ok := aliases[name]; ok {
		return a
	}
	return name
}

// handleSlash parses and dispatches a typed slash command. Unknown commands
// post a system message back to chat.
func (s *Screen) handleSlash(text string) (*Screen, tea.Cmd) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return s, nil
	}
	name := canonicalName(parts[0])
	args := parts[1:]

	switch name {
	case "/spotlight":
		return s, screens.Navigate(screens.SpotlightID)
	case "/blitz":
		return s.cmdBlitz()
	case "/themes":
		return s.cmdThemes(args)
	case "/help":
		return s.cmdHelp()
	case "/clear":
		return s.cmdClear()
	case "/quit", "/leave":
		// Broadcast that this session is ending so the dispatcher can
		// Release the sandbox + worktree before we bring the screen
		// down.
		s.broadcastLeave("quit")
		return s, screens.Quit()
	case "/me":
		return s.cmdMe(args)
	case "/who":
		return s.cmdWho()
	}
	s.postSystem(fmt.Sprintf("unknown command %q, try /help", parts[0]))
	return s, nil
}

// ---------------------------------------------------------------------------
// Per-command handlers, kept as methods on *Screen so they have direct access
// to the channel list and posting helpers.
// ---------------------------------------------------------------------------

func (s *Screen) cmdHelp() (*Screen, tea.Cmd) {
	lines := []string{"available commands:"}
	for _, c := range builtins {
		lines = append(lines, fmt.Sprintf("  %-13s %s", c.name, c.desc))
	}
	lines = append(lines, "navigation: esc returns to lobby from any screen · ctrl+c quits")
	s.postSystem(strings.Join(lines, "\n"))
	return s, nil
}

func (s *Screen) cmdClear() (*Screen, tea.Cmd) {
	if ch := s.activeChannel(); ch != nil {
		ch.Messages = nil
	}
	return s, nil
}

func (s *Screen) cmdMe(args []string) (*Screen, tea.Cmd) {
	body := strings.Join(args, " ")
	if body == "" {
		return s, nil
	}
	if ch := s.activeChannel(); ch != nil {
		ch.Messages = append(ch.Messages, ui.ChatMessage{
			Author: s.nick, Body: body, At: time.Now(), Kind: ui.ChatAction,
		})
		s.chatScroll = 0
	}
	return s, nil
}

// cmdBlitz starts a thirty-second cascade-race round. The blitz picks a
// target word at construction; the lobby announces it via a mod
// ChatAction whose Settle macro scrambles for 600ms then cascade-settles
// the target word into place, the cascade engine being the announcement.
// New chat that arrives during the round gets scored against the target;
// matches post a mod +N confirmation that also cascade-settles in.
func (s *Screen) cmdBlitz() (*Screen, tea.Cmd) {
	if s.blitz != nil {
		s.postSystem("a blitz is already running")
		return s, nil
	}
	s.blitz = games.NewBlitz(time.Now())
	target := s.blitz.Target()
	s.postSystem("blitz running for thirty seconds.")
	s.postMod("type " + target + " fast.")
	// Force a full repaint so the new banner survives the alt-screen
	// diff that otherwise skips frames when only state changes.
	return s, tea.ClearScreen
}

// cmdThemes lists the registered color themes when called bare, or
// switches to the requested one when given an argument. Switching forces
// a full repaint so the alt-screen renderer picks up the new colors on
// the next frame instead of waiting for a content change.
func (s *Screen) cmdThemes(args []string) (*Screen, tea.Cmd) {
	if len(args) == 0 {
		lines := []string{"themes:"}
		for _, name := range theme.ListThemes() {
			prefix := "  "
			if name == theme.Active {
				prefix = "* "
			}
			lines = append(lines, prefix+name)
		}
		lines = append(lines, "switch: /themes <name>")
		s.postSystem(strings.Join(lines, "\n"))
		return s, nil
	}
	name := strings.ToLower(args[0])
	if !theme.ApplyByName(name) {
		s.postSystem(fmt.Sprintf("unknown theme %q, try /themes for the list", name))
		return s, nil
	}
	s.postSystem(fmt.Sprintf("theme: %s", name))
	return s, tea.ClearScreen
}

// broadcastLeave publishes a PresenceLeft event with the supplied
// reason so the dispatcher can release the actor's sandbox and any
// other room services tied to the session can tear down. Safe to
// call when no broker is attached (local mode).
func (s *Screen) broadcastLeave(reason string) {
	if s.broker == nil {
		return
	}
	actor := events.Actor{
		ID:          s.principal.ID,
		DisplayName: s.principal.DisplayName,
		Kind:        s.principal.Kind.String(),
		SessionID:   s.principal.SessionID,
	}
	_ = s.broker.PublishEvent(events.NewPresenceLeft("", actor, reason))
}

func (s *Screen) cmdWho() (*Screen, tea.Cmd) {
	ch := s.activeChannel()
	if ch == nil {
		return s, nil
	}
	if s.broker == nil {
		s.postSystem("here right now: " + s.nick + " (local)")
		return s, nil
	}
	actors := s.broker.Presence()
	if len(actors) == 0 {
		s.postSystem("nobody here yet but you. type something so others see you arrived.")
		return s, nil
	}
	names := make([]string, 0, len(actors))
	for _, a := range actors {
		if a.DisplayName == "" {
			continue
		}
		names = append(names, a.DisplayName)
	}
	if len(names) == 0 {
		s.postSystem("here right now: (no display names)")
		return s, nil
	}
	s.postSystem("here right now: " + strings.Join(names, " "))
	return s, nil
}

