// Package lobby is the chat-style entry point that doubles as the app's home
// screen. It owns the channel list, manages an always-focused input, and
// routes slash commands to other screens via screens.Navigate.
//
// Files in this package:
//   - lobby.go    , Screen type, Init/Update/View, message posting
//   - commands.go , slash command registry + per-command handlers
//   - completion.go, Tab autocomplete
//   - seed.go     , fake channels + the @boggy username
package lobby

import (
	"fmt"
	"strings"
	"time"

	"github.com/bchayka/gitstatus/internal/config"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/field"
	"github.com/bchayka/gitstatus/internal/games"
	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/mod"
	"github.com/bchayka/gitstatus/internal/room"
	"github.com/bchayka/gitstatus/internal/screens"
	"github.com/bchayka/gitstatus/internal/theme"
	"github.com/bchayka/gitstatus/internal/typo"
	"github.com/bchayka/gitstatus/internal/ui"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

// Screen is the lobby's own state. The chat input is always focused, so this
// screen reports InputFocused()==true and the app's global key handlers stay
// out of the way.
type Screen struct {
	principal identity.Principal
	nick      string

	channels   []Channel
	chatActive int
	chatScroll int

	input      textinput.Model
	history    []string
	historyIdx int
	paletteIdx int // selection inside the command palette popup

	joinPosted bool

	// backdrop drives the field engine behind the chat scrollback. Each
	// keystroke pulses its motion accumulator so typing produces palette
	// drift the same way mouse motion does on the ertdfgcvb site.
	backdrop *field.Backdrop

	// tier state: hypeUntil holds the deadline for a tier-3 burst triggered
	// by chat arrivals; lastChatEvent is used to drop to tier 0 on long
	// silences.
	hypeUntil     time.Time
	lastChatEvent time.Time

	mod *mod.Mod

	// broker hands the shared #lobby scrollback across SSH sessions. When
	// nil the lobby runs fully local, useful for `go run .` and tests.
	broker    *room.Broker
	roomSub   <-chan events.Event
	roomSubID room.SubscriberID
	lobbyIdx  int

	// choreographer drives event-triggered cell animations (waves, awards,
	// gathers). Lobby renders any active CellTransforms over the chat
	// scrollback area each frame.
	choreographer    *typo.Choreographer
	activeTransforms []typo.CellTransform // updated each Tick; consumed in View
	lastPlacements   []msgPlacement       // chat message body Layouts + positions from last View

	// cfg carries the team's room configuration (brand, spotlight content,
	// moderator personality). The flagship loads config.DefaultVibespace();
	// other teams load their own. Surfaces and rendering read from here so
	// the same engine paints any team's room.
	cfg config.RoomConfig

	// blitz is the in-flight round when a /blitz is running. nil otherwise.
	// The round does not own a grid, the View loop calls blitz.Paint on
	// each visible chat line's AnimationState every frame, which pushes
	// the chat itself into a sustained scramble + tint + bob. New chat
	// posts normally during the round and feeds the winner tally via
	// blitz.OnNewMessage. Once blitz.Done flips, the lobby names the
	// winner and clears the field.
	blitz *games.Blitz

	// inflightCommands tracks /run and /sh commands the local actor or
	// other session participants have issued. Keyed by CommandID so
	// SandboxCommandOutput frames can flush full lines to the right
	// channel and preserve mid-chunk partial lines.
	inflightCommands map[uuid.UUID]*inflightCommand
}

// inflightCommand carries the per-command state needed to render
// streaming SandboxCommandOutput events as orderly chat lines.
type inflightCommand struct {
	channel    string
	authorTag  string
	stdoutBuf  []byte
	stderrBuf  []byte
}

// msgPlacement records where one chat message's body lives in the rendered
// scrollback. /wave + future event handlers use this to schedule transforms
// on real chat cells.
type msgPlacement struct {
	Layout       *typo.Layout
	Body         string
	PrefixWidth  int
	FlatRowStart int
}

// New constructs a fresh lobby with seeded channels and a focused input.
// principal is the verified identity for this session; broker is the
// shared room state and may be nil for fully-local mode. When broker is
// attached every channel's scrollback comes from broker.Snapshot and a
// single subscription delivers events for all channels.
func New(principal identity.Principal, broker *room.Broker, cfg config.RoomConfig) *Screen {
	nick := principal.DisplayName
	if nick == "" {
		nick = "@boggy"
	}
	s := &Screen{
		principal:        principal,
		nick:             nick,
		channels:         seedChannels(),
		chatActive:       0,
		input:            newInput(),
		backdrop:         field.NewBackdrop(),
		mod:              mod.New(),
		broker:           broker,
		lobbyIdx:         -1,
		choreographer:    typo.NewChoreographer(),
		cfg:              cfg,
		inflightCommands: map[uuid.UUID]*inflightCommand{},
	}
	for i, ch := range s.channels {
		if ch.Name == "#lobby" {
			s.lobbyIdx = i
			break
		}
	}
	if broker != nil {
		for i, ch := range s.channels {
			if msgs := broker.Snapshot(ch.Name); len(msgs) > 0 {
				s.channels[i].Messages = msgs
			}
		}
		s.roomSubID, s.roomSub = broker.Subscribe()
	}
	return s
}

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Prompt = ""
	ti.CharLimit = 0
	ti.Placeholder = "message #lobby or type /help"
	ti.Focus()
	return ti
}

func (s *Screen) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, field.TickCmd()}
	if c := s.waitForRoom(); c != nil {
		cmds = append(cmds, c)
	}
	return tea.Batch(cmds...)
}

// roomEventMsg wraps an events.Event so the lobby can handle it in Update.
type roomEventMsg struct {
	evt events.Event
}

// waitForRoom returns the Bubbletea command that blocks on the broker
// subscription. Each broker event is delivered as one roomEventMsg; Update
// re-issues the command to keep listening.
func (s *Screen) waitForRoom() tea.Cmd {
	if s.roomSub == nil {
		return nil
	}
	sub := s.roomSub
	return func() tea.Msg {
		evt, ok := <-sub
		if !ok {
			return nil
		}
		return roomEventMsg{evt: evt}
	}
}

// Close unsubscribes from the broker. Called by the router when the
// session ends so we don't leak channels across reconnects.
func (s *Screen) Close() {
	if s.broker != nil && s.roomSub != nil {
		s.broker.Unsubscribe(s.roomSubID)
		s.roomSub = nil
	}
}

func (s *Screen) Name() string  { return screens.LobbyID }
func (s *Screen) Title() string { return "lobby" }

func (s *Screen) HeaderContext() string {
	ch := s.activeChannel()
	if ch == nil {
		return ""
	}
	sep := lipgloss.NewStyle().Foreground(theme.Muted).Render(" · ")
	return lipgloss.NewStyle().Foreground(theme.OK).Render(ch.Name) + sep +
		lipgloss.NewStyle().Foreground(theme.Muted).Render(fmt.Sprintf("%d online", ch.Online))
}

func (s *Screen) Footer() []screens.KeyHint {
	if s.paletteVisible() {
		return []screens.KeyHint{
			{Key: "↑/↓", Desc: "navigate"},
			{Key: "tab", Desc: "fill"},
			{Key: "enter", Desc: "run"},
			{Key: "esc", Desc: "cancel"},
		}
	}
	return []screens.KeyHint{
		{Key: "enter", Desc: "send"},
		{Key: "/", Desc: "commands"},
		{Key: "↑/↓", Desc: "history"},
		{Key: "pgup/pgdn", Desc: "scroll"},
		{Key: "ctrl+c", Desc: "quit"},
	}
}

func (s *Screen) InputFocused() bool { return true }

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (s *Screen) Update(msg tea.Msg) (screens.Screen, tea.Cmd) {
	switch m := msg.(type) {
	case tea.KeyMsg:
		s.backdrop.Pulse(0.04)
		return s.handleKey(m)
	case tea.MouseMsg:
		s.backdrop.SetCursor(float64(m.X), float64(m.Y))
		return s, nil
	case roomEventMsg:
		s.handleRoomEvent(m.evt)
		s.hypeUntil = time.Now().Add(5 * time.Second)
		s.lastChatEvent = time.Now()
		return s, s.waitForRoom()
	case field.TickMsg:
		t := time.Time(m)
		s.backdrop.Tick(t)
		s.updateTier(t)
		s.activeTransforms = s.choreographer.Tick(t)
		if s.blitz != nil {
			s.blitz.Tick(t)
			// Winner posts at the start of the offset ramp, not at Done.
			// The mod ChatAction's Settle macro cascades the nick into
			// place while the dance dims around it; if we waited for
			// Done the cascade would land in a quiet room.
			if winner, ready := s.blitz.WinnerReady(); ready {
				s.endBlitz(t, winner)
			}
			if s.blitz != nil && s.blitz.Done() {
				s.blitz = nil
			}
		}
		if s.broker == nil {
			if line := s.mod.Tick(t); line != "" {
				s.postMod(line)
			}
		}
		return s, field.TickCmd()
	}
	return s, nil
}

func (s *Screen) handleKey(msg tea.KeyMsg) (screens.Screen, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return s, tea.Quit
	case "enter":
		// When the palette is open, Enter accepts the highlighted command ,
		// it's inserted in the input then submitted in one keystroke, matching
		// the muscle memory of Claude Code / VS Code command palettes.
		if cmd := s.acceptPalette(); cmd != "" {
			s.input.SetValue(cmd)
		}
		s.resetPalette()
		return s.submit()
	case "tab":
		// Tab fills the input without submitting, so users can pick a command
		// like /join and then type the channel name.
		if s.paletteVisible() {
			s.fillPalette()
		}
		return s, nil
	case "shift+tab":
		if s.paletteVisible() {
			s.movePalette(-1)
		}
		return s, nil
	case "up":
		if s.paletteVisible() {
			s.movePalette(-1)
		} else {
			s.recallHistory(-1)
		}
		return s, nil
	case "down":
		if s.paletteVisible() {
			s.movePalette(+1)
		} else {
			s.recallHistory(+1)
		}
		return s, nil
	case "pgup":
		s.chatScroll += 5
		return s, nil
	case "pgdown":
		s.chatScroll -= 5
		if s.chatScroll < 0 {
			s.chatScroll = 0
		}
		return s, nil
	case "esc":
		s.input.SetValue("")
		s.resetPalette()
		return s, nil
	}
	// Any other key edits the input → the filtered match list will change, so
	// reset the highlight back to the top of the new list.
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	s.resetPalette()
	return s, cmd
}

func (s *Screen) submit() (screens.Screen, tea.Cmd) {
	text := strings.TrimSpace(s.input.Value())
	s.input.SetValue("")
	if text == "" {
		return s, nil
	}
	s.history = append(s.history, text)
	s.historyIdx = len(s.history)

	if strings.HasPrefix(text, "/") {
		ss, cmd := s.handleSlash(text)
		return ss, cmd
	}
	s.postUser(text)
	return s, nil
}

func (s *Screen) recallHistory(delta int) {
	if len(s.history) == 0 {
		return
	}
	switch {
	case delta < 0:
		if s.historyIdx > 0 {
			s.historyIdx--
		}
	case delta > 0:
		if s.historyIdx < len(s.history) {
			s.historyIdx++
		}
	}
	if s.historyIdx >= len(s.history) {
		s.input.SetValue("")
		return
	}
	s.input.SetValue(s.history[s.historyIdx])
	s.input.CursorEnd()
}

// ---------------------------------------------------------------------------
// Posting helpers, used by both regular sends and slash command handlers
// ---------------------------------------------------------------------------

func (s *Screen) activeChannel() *Channel {
	if s.chatActive < 0 || s.chatActive >= len(s.channels) {
		return nil
	}
	return &s.channels[s.chatActive]
}

func (s *Screen) postUser(body string) {
	ch := s.activeChannel()
	if ch == nil {
		return
	}
	now := time.Now()
	msg := ui.ChatMessage{Author: s.nick, Body: body, At: now}
	if s.broker != nil {
		s.broker.Publish(ch.Name, msg)
		return
	}
	ch.Messages = append(ch.Messages, msg)
	s.chatScroll = 0
	s.mod.NoteChat(now)

	// Local-mode blitz scoring: with no broker there's no roomEvent
	// loop calling handleRoomEvent, so the scoring fires here directly.
	// SSH mode is covered by the handleRoomEvent path.
	if s.blitz != nil {
		if points, matched := s.blitz.MatchScore(msg.Author, msg.Body); matched {
			s.postMod(fmt.Sprintf("%s +%d", msg.Author, points))
		}
	}
}

// updateTier maps room state onto the field's five intensity tiers. With a
// broker attached we defer to broker.Tier() so every screen sees the same
// energy level the mod sees; locally we fall back to a smaller idle/burst
// heuristic.
func (s *Screen) updateTier(t time.Time) {
	if s.broker != nil {
		s.backdrop.SetTier(s.broker.Tier())
		return
	}
	switch {
	case t.Before(s.hypeUntil):
		s.backdrop.SetTier(3)
	case !s.lastChatEvent.IsZero() && t.Sub(s.lastChatEvent) > 30*time.Second:
		s.backdrop.SetTier(0)
	default:
		s.backdrop.SetTier(1)
	}
}

// endBlitz announces the winner at the entry to the offset ramp-down.
// The dance is still painting (intensity tapering from 1 → 0), and the
// winner's nick posts as a mod ChatAction whose Settle macro cascades
// the name into place over a second. The result reads as the dance's
// echo: the room dims, the winner emerges.
//
// The lobby clears s.blitz separately once blitz.Done() flips at the
// end of the offset window, so the chat returns to its quiet baseline
// only after the surface has fully unwound.
func (s *Screen) endBlitz(t time.Time, winner string) {
	if winner == "" {
		winner = "the room"
	}
	s.postMod("that round was " + winner + ".")
}

// handleRoomEvent applies a broker event to the local channel mirror so the
// lobby renders the same scrollback every other session sees. Some kinds
// (joins, mod posts) also trigger a foreground cascade so the engine
// announces them visibly on top of the field.
func (s *Screen) handleRoomEvent(evt events.Event) {
	switch e := evt.(type) {
	case *events.ChatPosted:
		s.handleChatPosted(e)
	case *events.PresenceJoined:
		// Phase 1: presence events are visible via the chat-side join
		// message until a dedicated presence pane lands in Phase 3.
	case *events.PresenceLeft:
		// Same as PresenceJoined; rendered as chat-side noise for now.
	case *events.ModTagged:
		// Phase 1 mod tags are still attached inline via the
		// ChatMessage.Tags field on the underlying ChatPosted. This
		// branch is the hook for future explicit tag events.
	case *events.SandboxCommandIssued:
		s.handleSandboxIssued(e)
	case *events.SandboxCommandOutput:
		s.handleSandboxOutput(e)
	case *events.SandboxCommandCompleted:
		s.handleSandboxCompleted(e)
	case *events.AgentSaid:
		s.handleAgentSaid(e)
	case *events.AgentToolCalled:
		s.handleAgentToolCalled(e)
	default:
		// Unknown events from a future build land here. Quietly drop;
		// the broker has already persisted them.
	}
}

// handleSandboxIssued records the command and renders an "@actor ran:
// argv" system line in the originating channel.
func (s *Screen) handleSandboxIssued(e *events.SandboxCommandIssued) {
	s.inflightCommands[e.CommandID] = &inflightCommand{
		channel:   e.Channel,
		authorTag: e.Actor.DisplayName,
	}
	body := fmt.Sprintf("%s ran %s", e.Actor.DisplayName, strings.Join(e.Argv, " "))
	s.appendSystem(e.Channel, body)
}

// handleSandboxOutput flushes complete lines from an output chunk into
// the originating channel as system messages. Partial trailing lines
// are buffered per (command, stream) and emitted when the next chunk
// arrives.
func (s *Screen) handleSandboxOutput(e *events.SandboxCommandOutput) {
	inf, ok := s.inflightCommands[e.CommandID]
	if !ok {
		// We may have missed the Issued event; fall back to the active
		// channel so output is at least visible somewhere.
		inf = &inflightCommand{channel: s.activeChannelName()}
		s.inflightCommands[e.CommandID] = inf
	}
	var buf *[]byte
	switch e.Stream {
	case events.StreamStderr:
		buf = &inf.stderrBuf
	default:
		buf = &inf.stdoutBuf
	}
	*buf = append(*buf, e.Chunk...)
	for {
		idx := indexOfByte(*buf, '\n')
		if idx < 0 {
			break
		}
		line := string((*buf)[:idx])
		*buf = (*buf)[idx+1:]
		s.appendSystem(inf.channel, "  "+line)
	}
}

// handleSandboxCompleted flushes any trailing partial line, prints the
// exit line, and deletes the inflight entry.
func (s *Screen) handleSandboxCompleted(e *events.SandboxCommandCompleted) {
	inf, ok := s.inflightCommands[e.CommandID]
	if !ok {
		return
	}
	if len(inf.stdoutBuf) > 0 {
		s.appendSystem(inf.channel, "  "+string(inf.stdoutBuf))
	}
	if len(inf.stderrBuf) > 0 {
		s.appendSystem(inf.channel, "  "+string(inf.stderrBuf))
	}
	if e.Error != "" {
		s.appendSystem(inf.channel, fmt.Sprintf("  error: %s", e.Error))
	}
	s.appendSystem(inf.channel, fmt.Sprintf("  exit %d", e.ExitCode))
	delete(s.inflightCommands, e.CommandID)
}

// handleAgentSaid renders an agent's text reply as a system line in
// the active channel. Authorship is preserved via the actor's display
// name on the event so multi-user rooms know whose agent spoke.
func (s *Screen) handleAgentSaid(e *events.AgentSaid) {
	channel := s.activeChannelName()
	display := e.Actor.DisplayName
	if display == "" {
		display = "agent"
	}
	s.appendSystem(channel, fmt.Sprintf("%s's agent: %s", display, e.Text))
}

// handleAgentToolCalled surfaces the tool the agent invoked. Useful
// for trust and review; rendered as a faint indented line so it does
// not dominate the channel.
func (s *Screen) handleAgentToolCalled(e *events.AgentToolCalled) {
	channel := s.activeChannelName()
	mark := "·"
	if e.IsError {
		mark = "!"
	}
	s.appendSystem(channel, fmt.Sprintf("  %s %s(%s)", mark, e.Tool, summarizeArgs(e.Args)))
}

// summarizeArgs returns a compact display of a JSON args blob: keeps
// it under 80 chars, truncates to "...".
func summarizeArgs(argsJSON string) string {
	if len(argsJSON) <= 80 {
		return argsJSON
	}
	return argsJSON[:77] + "..."
}

// appendSystem appends a ChatSystem message to the named channel's
// scrollback. No-op for unknown channels.
func (s *Screen) appendSystem(channel, body string) {
	for i := range s.channels {
		if s.channels[i].Name != channel {
			continue
		}
		s.channels[i].Messages = append(s.channels[i].Messages, ui.ChatMessage{
			Author: "*",
			Body:   body,
			At:     time.Now(),
			Kind:   ui.ChatSystem,
		})
		if i == s.chatActive {
			s.chatScroll = 0
		}
		return
	}
}

// indexOfByte returns the index of the first occurrence of c in b, or
// -1. Pure-Go equivalent of bytes.IndexByte; kept inline so this file
// does not need an extra import for one call.
func indexOfByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}

// ensureUUIDLink is a static compile-time check that the uuid import
// is exercised; the inflightCommands map's key type pins it.
var _ = uuid.UUID{}

// handleChatPosted is the legacy ChatMessage path under the new typed
// envelope. Materializes the event into a ui.ChatMessage and runs the
// existing channel-routing, blitz-scoring, and backdrop-cascade logic.
func (s *Screen) handleChatPosted(e *events.ChatPosted) {
	msg := e.AsChatMessage()
	for i := range s.channels {
		if s.channels[i].Name != e.Channel {
			continue
		}
		s.channels[i].Messages = append(s.channels[i].Messages, msg)
		if i == s.chatActive {
			s.chatScroll = 0
		}
		break
	}
	if e.Channel != s.activeChannelName() {
		return
	}
	if s.blitz != nil && msg.Kind == ui.ChatNormal && msg.Author != mod.Nick {
		if points, matched := s.blitz.MatchScore(msg.Author, msg.Body); matched {
			s.postMod(fmt.Sprintf("%s +%d", msg.Author, points))
		}
	}
	switch {
	case msg.Kind == ui.ChatJoin:
		s.backdrop.AddCascade(field.CascadeLine{
			Row:   0,
			Text:  msg.Author + " joined",
			Decay: 4 * time.Second,
		})
	case msg.Kind == ui.ChatAction && msg.Author == mod.Nick:
		s.backdrop.AddCascade(field.CascadeLine{
			Row:   1,
			Text:  mod.Nick + " · " + msg.Body,
			Decay: 5 * time.Second,
		})
	}
}

// activeChannelName returns the name of the active channel, or "" if none.
func (s *Screen) activeChannelName() string {
	if ch := s.activeChannel(); ch != nil {
		return ch.Name
	}
	return ""
}

// postMod posts a moderator line to the active channel. Visually identical
// to ChatAction (italic accent2) but with the @mod author convention.
func (s *Screen) postMod(body string) {
	ch := s.activeChannel()
	if ch == nil {
		return
	}
	now := time.Now()
	ch.Messages = append(ch.Messages, ui.ChatMessage{
		Author: mod.Nick, Body: body, At: now, Kind: ui.ChatAction,
	})
	s.chatScroll = 0
	s.mod.NoteChat(now)
}

func (s *Screen) postSystem(body string) {
	ch := s.activeChannel()
	if ch == nil {
		return
	}
	for _, line := range strings.Split(body, "\n") {
		ch.Messages = append(ch.Messages, ui.ChatMessage{
			Author: "*", Body: line, At: time.Now(), Kind: ui.ChatSystem,
		})
	}
	s.chatScroll = 0
}

// EnsureJoined posts the "entered the chat" join message once, then no-ops on
// subsequent calls. Called by the router when transitioning from intro. When
// a broker is attached the join broadcasts so other sessions see the new
// nick and the broker's mod auto-welcomes the room.
func (s *Screen) EnsureJoined() {
	if s.joinPosted {
		return
	}
	now := time.Now()
	joinMsg := ui.ChatMessage{
		Author: s.nick, Body: "entered the chat", At: now, Kind: ui.ChatJoin,
	}
	if s.broker != nil {
		s.broker.Publish("#lobby", joinMsg)
	} else if ch := s.activeChannel(); ch != nil {
		ch.Messages = append(ch.Messages, joinMsg)
		s.postMod(s.mod.Welcome(s.nick))
	}
	s.joinPosted = true
	s.chatScroll = 0
}

// OnEnter is the router's field-driven entry hook. We pulse the backdrop hard
// and register the user's nick as a foreground line so it flap-spins across
// the field; both decay naturally over a few seconds.
func (s *Screen) OnEnter() {
	s.backdrop.AddCascade(field.CascadeLine{
		Row:   0,
		Text:  s.nick + " · the workshop is open",
		Decay: 5 * time.Second,
	})
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (s *Screen) View(width, height int) string {
	w := ui.FeedShellWidth(width)
	contentW := w - 2

	if s.chatActive < 0 || s.chatActive >= len(s.channels) {
		s.chatActive = 0
	}
	ch := s.channels[s.chatActive]

	bar := s.renderTopBar(ch, contentW)
	barH := lipgloss.Height(bar)

	prompt := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).
		Render("[" + strings.TrimPrefix(s.nick, "@") + "]> ")
	s.input.Width = contentW - lipgloss.Width(prompt) - 1
	inputLine := prompt + s.input.View()

	palette := s.renderPalette(contentW)
	paletteH := s.paletteHeight()

	// Layout (bottom-anchored input):
	//   bar (1) · [blitz banner (1)?] · divider (1) · scrollback (chatH) · divider (1) · palette (paletteH) · input (1)
	// Total fixed chrome is 4 rows + the optional blitz banner; scrollback
	// flexes around it.
	bannerH := 0
	if s.blitz != nil {
		bannerH = 1
	}
	chatH := height - barH - paletteH - bannerH - 4
	if chatH < 4 {
		chatH = 4
	}

	now := time.Now()

	// Build chat rows. Track each message's body Layout and where in the
	// flat row stream its body lands, so the choreographer can target real
	// chat cells (not phantom demo layouts) when an effect fires.
	blitzActive := s.blitz != nil
	total := len(ch.Messages)
	var lines []string
	placements := make([]msgPlacement, 0, len(ch.Messages))
	for i, msg := range ch.Messages {
		var paint func(*typo.AnimationState)
		if blitzActive {
			idx := i
			paint = func(state *typo.AnimationState) {
				s.blitz.Paint(state, idx, total, now)
			}
		}
		rendered, body, prefixW := renderChatLineAnimDetailed(msg, contentW, now, blitzActive, paint)
		flatStart := len(lines)
		lines = append(lines, rendered...)
		layoutID := msgKey(msg)
		layout := typo.Prepare(layoutID, body, contentW-prefixW-1)
		placements = append(placements, msgPlacement{
			Layout:       layout,
			Body:         body,
			PrefixWidth:  prefixW,
			FlatRowStart: flatStart,
		})
	}
	s.lastPlacements = placements
	chatRows := scrollbackRows(lines, chatH, s.chatScroll)

	// Field engine grid is no longer rendered behind chat, the substrate
	// is the chat itself, animated via the choreographer. Empty rows stay
	// empty; transforms render directly on top of the chat string.
	visible := strings.Join(chatRows, "\n")

	// Render active CellTransforms on top of the chat, borrowed cells
	// move across the chat area; the original positions are blanked so
	// you see the actual chat text travelling, not a phantom duplicate.
	if len(s.activeTransforms) > 0 {
		visible = s.composeTransformOverlay(visible, contentW, chatH, now, placements)
	}

	parts := []string{
		bar,
	}
	if s.blitz != nil {
		bannerStyle := lipgloss.NewStyle().
			Background(theme.Accent).
			Foreground(theme.Bg).
			Bold(true).
			Width(contentW).
			Padding(0, 1)
		remain := s.blitzRemainingDisplay()
		if remain == "" {
			remain = "0:00"
		}
		bannerText := fmt.Sprintf("BLITZ · TARGET %s · %s", strings.ToUpper(s.blitz.Target()), remain)
		parts = append(parts, bannerStyle.Render(bannerText))
	}
	parts = append(parts, ui.Divider(contentW), visible, ui.Divider(contentW))
	if palette != "" {
		parts = append(parts, palette)
	}
	parts = append(parts, inputLine)
	stacked := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.Place(width, height, lipgloss.Left, lipgloss.Top, stacked)
}

func (s *Screen) renderTopBar(ch Channel, width int) string {
	brand := lipgloss.NewStyle().Foreground(theme.Fg).Bold(true).Render(s.cfg.Brand.Name)
	bar := brand
	sep := lipgloss.NewStyle().Foreground(theme.Muted).Render("  ·  ")
	// While a blitz round is in flight the top bar takes over: the target
	// word renders in phosphor green so the room can read it at a glance
	// without waiting for the mod cascade to settle, plus a countdown and
	// the live leaderboard sit alongside. The spotlight slot returns once
	// the round is fully resolved.
	if s.blitz != nil {
		target := s.blitz.Target()
		targetLabel := lipgloss.NewStyle().Foreground(theme.Muted).Render("TARGET")
		targetWord := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(target)
		bar += sep + targetLabel + " " + targetWord
		if remain := s.blitzRemainingDisplay(); remain != "" {
			countdown := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true).Render(remain)
			bar += " " + countdown
		}
		if leaderboard := s.blitzLeaderboardDisplay(); leaderboard != "" {
			bar += sep + leaderboard
		}
		if lipgloss.Width(bar) > width {
			bar = ui.Truncate(bar, width)
		}
		return bar
	}
	if s.cfg.Surfaces.Spotlight && s.cfg.Spotlight.Name != "" {
		tonight := lipgloss.NewStyle().Foreground(theme.Accent2).Render("TONIGHT")
		spotlight := lipgloss.NewStyle().Foreground(theme.Fg).Bold(true).Render(s.cfg.Spotlight.Name)
		bar += sep + tonight + " " + spotlight
		if s.cfg.Spotlight.Author != "" {
			by := lipgloss.NewStyle().Foreground(theme.Muted).Italic(true).
				Render("by " + s.cfg.Spotlight.Author)
			bar += " " + by
		}
	}
	online := lipgloss.NewStyle().Foreground(theme.Muted).
		Render(fmt.Sprintf("%d here", ch.Online))
	bar += sep + online
	if lipgloss.Width(bar) > width {
		bar = ui.Truncate(bar, width)
	}
	return bar
}

// blitzRemainingDisplay returns a short countdown string ("0:14") for the
// top-bar HUD, or "" if the round is no longer running.
func (s *Screen) blitzRemainingDisplay() string {
	if s.blitz == nil {
		return ""
	}
	remain := s.blitz.Remaining(time.Now())
	if remain <= 0 {
		return ""
	}
	secs := int(remain.Seconds())
	return fmt.Sprintf("0:%02d", secs)
}

// blitzLeaderboardDisplay renders the top three scorers in the top bar
// during a round. Leader's nick is in phosphor green; the rest are
// parchment cream. Returns "" when no one has scored yet so the bar
// stays clean.
func (s *Screen) blitzLeaderboardDisplay() string {
	if s.blitz == nil {
		return ""
	}
	standings := s.blitz.Standings()
	if len(standings) == 0 {
		return ""
	}
	parts := []string{}
	for i, score := range standings {
		if i >= 3 {
			break
		}
		nick := strings.TrimPrefix(score.Author, "@")
		styled := lipgloss.NewStyle().Foreground(theme.Fg).Render(nick)
		if i == 0 {
			styled = lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(nick)
		}
		pts := lipgloss.NewStyle().Foreground(theme.Muted).Render(fmt.Sprintf(" %d", score.Points))
		parts = append(parts, styled+pts)
	}
	return strings.Join(parts, "  ")
}

// composeTransformOverlay renders active CellTransforms over the chat
// scrollback. Origins are derived from the placements collected during
// chat-row construction so the borrowed cells move FROM their real
// chat-screen position OUTWARD, and back. The original cell positions
// are blanked in the base string so the user sees one set of moving
// chars, not a ghost duplicate.
func (s *Screen) composeTransformOverlay(base string, width, height int, now time.Time, placements []msgPlacement) string {
	// Resolve each Layout ID to its on-screen origin in chat-area coords.
	totalRows := 0
	for _, p := range placements {
		// Placement's body row count is the rendered rows minus 0 (prefix
		// is on row 0; we'll align the body to the same first row).
		totalRows += p.Layout.Height
	}
	// Top padding when chat is shorter than the area (bottom-aligned).
	topPad := 0
	if flat := totalFlatRows(placements); flat < height {
		topPad = height - flat
	}

	origins := map[string]typo.LayoutOrigin{}
	for _, p := range placements {
		y := topPad + p.FlatRowStart
		x := p.PrefixWidth + 1
		origins[p.Layout.ID] = typo.LayoutOrigin{Layout: p.Layout, X: x, Y: y}
	}

	// Build the overlay grid.
	comp := typo.NewCompositor(width, height)
	comp.DrawTransforms(s.activeTransforms, origins, now)
	overlay := comp.Render()

	// Blank the natural cell positions for any cell currently in transform
	// so the chat text appears to physically leave its spot.
	borrowedByLayout := typo.IndexTransforms(s.activeTransforms)
	base = blankBorrowedCells(base, origins, borrowedByLayout, height)

	return overlayRows(base, overlay, height)
}

// totalFlatRows sums the rendered heights of all placements (each Layout
// reports its own wrapped row count via Height).
func totalFlatRows(placements []msgPlacement) int {
	total := 0
	for _, p := range placements {
		total += p.Layout.Height
	}
	return total
}

// blankBorrowedCells walks each transformed cell, computes its natural
// (col, row) in the base chat string, and replaces that visible cell with
// a space. The transformed copy renders elsewhere via the overlay.
func blankBorrowedCells(base string, origins map[string]typo.LayoutOrigin, borrowed map[string]map[int]bool, height int) string {
	rows := splitToHeight(base, height)
	for layoutID, idxSet := range borrowed {
		origin, ok := origins[layoutID]
		if !ok {
			continue
		}
		for idx := range idxSet {
			if idx < 0 || idx >= len(origin.Layout.Cells) {
				continue
			}
			cell := origin.Layout.Cells[idx]
			col := origin.X + cell.Col
			row := origin.Y + cell.Row
			if row < 0 || row >= len(rows) {
				continue
			}
			rows[row] = blankCellAt(rows[row], col)
		}
	}
	return strings.Join(rows, "\n")
}

// blankCellAt replaces the visible char at the given column with a space
// while preserving every ANSI escape sequence in the row. The styled
// envelope around the cell is kept; only the rune is swapped.
func blankCellAt(row string, col int) string {
	var b strings.Builder
	visibleCol := 0
	inEsc := false
	for _, r := range row {
		if r == '\x1b' {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if inEsc {
			b.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if visibleCol == col {
			b.WriteRune(' ')
		} else {
			b.WriteRune(r)
		}
		visibleCol++
	}
	return b.String()
}

// overlayRows merges two equal-height multi-line strings, taking the
// overlay's non-blank cells in preference to base. Base ANSI styling is
// preserved everywhere the overlay doesn't write.
func overlayRows(base, overlay string, height int) string {
	baseRows := splitToHeight(base, height)
	overRows := splitToHeight(overlay, height)
	out := make([]string, height)
	for i := 0; i < height; i++ {
		b, o := baseRows[i], overRows[i]
		if strings.TrimSpace(stripAnsi(o)) == "" {
			out[i] = b
			continue
		}
		out[i] = overlayRow(b, o)
	}
	return strings.Join(out, "\n")
}

func splitToHeight(s string, height int) []string {
	rows := strings.Split(s, "\n")
	if len(rows) < height {
		for len(rows) < height {
			rows = append(rows, "")
		}
	}
	if len(rows) > height {
		rows = rows[:height]
	}
	return rows
}

// overlayRow ANSI-aware overlay: walks base preserving its escape sequences,
// and at every visible column where `over` has a non-space char, writes the
// overlay char styled bold accent (the "in transit" treatment).
func overlayRow(base, over string) string {
	overChars := visibleColMap(over)
	if len(overChars) == 0 {
		return base
	}
	overlayStyle := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true)

	var b strings.Builder
	visibleCol := 0
	inEsc := false
	for _, r := range base {
		if r == '\x1b' {
			inEsc = true
			b.WriteRune(r)
			continue
		}
		if inEsc {
			b.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if ch, ok := overChars[visibleCol]; ok {
			b.WriteString(overlayStyle.Render(string(ch)))
		} else {
			b.WriteRune(r)
		}
		visibleCol++
	}
	// If overlay extends past base width, append remaining overlay chars
	// styled, the borrowed cells may have flown past the natural row end.
	for col, ch := range overChars {
		if col >= visibleCol {
			pad := col - visibleCol
			if pad > 0 {
				b.WriteString(strings.Repeat(" ", pad))
			}
			b.WriteString(overlayStyle.Render(string(ch)))
			visibleCol = col + 1
		}
	}
	return b.String()
}

// visibleColMap returns a map of visible-column → rune for non-space chars
// in an ANSI-styled string. Used to locate overlay cells by column.
func visibleColMap(s string) map[int]rune {
	out := map[int]rune{}
	visibleCol := 0
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r != ' ' {
			out[visibleCol] = r
		}
		visibleCol++
	}
	return out
}

// stripAnsi removes ANSI SGR sequences from a string, leaving raw runes.
// Used by the overlay merge so we can compare visible columns. Kept local
// to lobby for now since the typo package already has one we'd want to
// consolidate to later.
func stripAnsi(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// scrollbackRows clamps the scroll offset and returns the visible slice as a
// height-length string slice. Empty positions are empty strings so the
// compositor can show the field behind them.
func scrollbackRows(lines []string, height, scroll int) []string {
	maxScroll := len(lines) - height
	if maxScroll < 0 {
		maxScroll = 0
	}
	if scroll > maxScroll {
		scroll = maxScroll
	}
	end := len(lines) - scroll
	start := end - height
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[start:end]
	// Bottom-align: pad the TOP with empty rows so chat hugs the bottom of
	// the scrollback area. Those empty rows are where the field shows.
	if len(visible) < height {
		pad := make([]string, height-len(visible))
		visible = append(pad, visible...)
	}
	return visible
}

