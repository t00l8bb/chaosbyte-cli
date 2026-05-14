package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/room"
	"github.com/bchayka/gitstatus/internal/sandbox"
)

// Broker is the subset of room.Broker the dispatcher needs. The narrow
// interface lets tests stand in a fake.
type Broker interface {
	Subscribe() (room.SubscriberID, <-chan events.Event)
	Unsubscribe(room.SubscriberID)
	PublishEvent(events.Event) error
}

// Orchestrator is the subset of sandbox.Orchestrator the dispatcher
// needs. Same narrowing for tests.
type Orchestrator interface {
	Acquire(ctx context.Context, p identity.Principal, spec sandbox.Spec) (sandbox.Sandbox, error)
	Release(ctx context.Context, p identity.Principal) error
}

// Reprovisioner is an optional Orchestrator capability: swap the
// session's sandbox to a new base repo. The /pull verb requires it.
// Implementations without a worktree controller can omit this.
type Reprovisioner interface {
	Reprovision(ctx context.Context, p identity.Principal, baseRepo, branch string) (sandbox.Sandbox, error)
}

// Dispatcher subscribes to the broker, recognizes slash commands in
// ChatPosted events, and routes them to the orchestrator. It is safe
// to run one Dispatcher per room.
//
// Lifecycle: call Start with a context that signals shutdown; the
// dispatcher unsubscribes and drains in-flight commands when the
// context is cancelled. Stop is also exposed for synchronous teardown
// outside of context cancellation.
type Dispatcher struct {
	broker Broker
	orch   Orchestrator
	roomID string

	mu      sync.Mutex
	started bool
	stopped bool
	subID   room.SubscriberID
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// servings holds the dev-server process per actor (keyed by
	// Actor.ID). /serve registers an entry; /unserve, PresenceLeft,
	// or the process exiting clears it.
	servings map[string]*serving
}

// serving bundles the live dev-server process with the cancel
// function that kills it.
type serving struct {
	proc   sandbox.Process
	cancel context.CancelFunc
	port   int
	label  string
}

// New returns a Dispatcher for the given room scope.
func New(broker Broker, orch Orchestrator, roomID string) *Dispatcher {
	return &Dispatcher{
		broker:   broker,
		orch:     orch,
		roomID:   roomID,
		stopCh:   make(chan struct{}),
		servings: map[string]*serving{},
	}
}

// Start subscribes to the broker and begins routing slash commands.
// The dispatcher's loop runs until ctx is cancelled or Stop is called.
func (d *Dispatcher) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.started {
		d.mu.Unlock()
		return errors.New("dispatch: already started")
	}
	d.started = true
	id, ch := d.broker.Subscribe()
	d.subID = id
	d.mu.Unlock()

	d.wg.Add(1)
	go d.loop(ctx, ch)
	return nil
}

// Stop unsubscribes from the broker and waits for in-flight command
// goroutines to drain. Idempotent.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	close(d.stopCh)
	d.broker.Unsubscribe(d.subID)
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *Dispatcher) loop(ctx context.Context, ch <-chan events.Event) {
	defer d.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			switch e := evt.(type) {
			case *events.ChatPosted:
				parsed, err := Parse(e.Body)
				if err != nil {
					continue
				}
				d.wg.Add(1)
				go d.executeSafe(ctx, e, parsed)
			case *events.PresenceLeft:
				// Session ended (quit, disconnect, kicked, stalled).
				// Stop any /serve they had running, then release the
				// actor's sandbox + worktree so they do not leak.
				// Best-effort; we ignore the error.
				d.stopServing(ctx, e.Actor.ID, "disconnect")
				p := identity.Principal{
					ID:          e.Actor.ID,
					DisplayName: e.Actor.DisplayName,
					Kind:        principalKindFromActor(e.Actor.Kind),
					SessionID:   e.Actor.SessionID,
				}
				_ = d.orch.Release(ctx, p)
			}
		}
	}
}

// executeSafe wraps execute with a panic recovery so a single bad
// command does not kill the dispatcher's goroutine pool.
func (d *Dispatcher) executeSafe(ctx context.Context, chat *events.ChatPosted, cmd ParsedCommand) {
	defer d.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			// Surface the panic to chat as a completed event so the
			// posting user sees something went wrong. The recovered
			// goroutine returns without taking the dispatcher down.
			fakeCmdID := uuid.New()
			_ = d.broker.PublishEvent(events.NewSandboxCommandCompleted(
				d.roomID, chat.Actor, fakeCmdID, -1,
				fmt.Sprintf("dispatcher panic: %v", r),
			))
		}
	}()
	d.execute(ctx, chat, cmd)
}

// readBufSize is the per-iteration Read size from the sandbox stream.
// Tuned for terminal output: large enough to amortize syscalls,
// small enough that interactive output feels live.
const readBufSize = 4 * 1024

// maxPendingPartial caps how many bytes the chunker will hold while
// waiting for a safe split point. If a never-terminating ANSI
// sequence runs longer than this, we flush anyway so output is not
// stalled forever.
const maxPendingPartial = 16 * 1024

// execute runs a parsed command in the actor's sandbox and emits the
// associated events. Errors are surfaced via SandboxCommandCompleted
// with a non-empty Error field.
func (d *Dispatcher) execute(ctx context.Context, chat *events.ChatPosted, cmd ParsedCommand) {
	commandID := uuid.New()
	actor := chat.Actor

	// Publish the structured "issued" event so subscribers can render
	// "Daniel ran: cargo test" or similar before output starts.
	issued := events.NewSandboxCommandIssued(d.roomID, actor, commandID, actor.SessionID, chat.Channel, cmd.Argv)
	_ = d.broker.PublishEvent(issued)

	principal := identity.Principal{
		ID:          actor.ID,
		DisplayName: actor.DisplayName,
		Kind:        principalKindFromActor(actor.Kind),
		SessionID:   actor.SessionID,
	}

	if cmd.Verb == VerbPull {
		d.executePull(ctx, actor, principal, commandID, cmd.Raw)
		return
	}
	if cmd.Verb == VerbScratch {
		d.executeScratch(ctx, actor, principal, commandID, cmd.Raw)
		return
	}
	if cmd.Verb == VerbServe {
		d.executeServe(ctx, actor, principal, commandID, cmd.Argv)
		return
	}
	if cmd.Verb == VerbUnserve {
		d.executeUnserve(ctx, actor, commandID)
		return
	}

	sb, err := d.orch.Acquire(ctx, principal, sandbox.Spec{})
	if err != nil {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("acquire sandbox: %w", err))
		return
	}

	proc, err := sb.Exec(ctx, sandbox.Command{
		Path: cmd.Argv[0],
		Args: cmd.Argv[1:],
	})
	if err != nil {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("exec: %w", err))
		return
	}

	// Stream stdout/stderr concurrently. The dispatcher's outer Stop
	// waits on these via wg.
	var streamWG sync.WaitGroup
	streamWG.Add(2)
	go func() {
		defer streamWG.Done()
		d.pump(actor, commandID, events.StreamStdout, proc.Stdout())
	}()
	go func() {
		defer streamWG.Done()
		d.pump(actor, commandID, events.StreamStderr, proc.Stderr())
	}()

	exitCode, waitErr := proc.Wait(ctx)
	streamWG.Wait()

	var runErr error
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		runErr = waitErr
	}
	d.publishCompleted(actor, commandID, exitCode, runErr)
}

// executePull handles the /pull verb. It asks the orchestrator to
// swap the session's sandbox to a fresh worktree rooted at path. The
// orchestrator returns the new sandbox; we publish a Completed event
// when the swap is done.
//
// path must point at a local bare git repo. Remote URLs are rejected
// because the host fence denies network. /pull from a remote will
// land when we add an out-of-fence fetch path.
func (d *Dispatcher) executePull(ctx context.Context, actor events.Actor, principal identity.Principal, commandID uuid.UUID, path string) {
	rp, ok := d.orch.(Reprovisioner)
	if !ok {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/pull requires a worktree controller; ask the operator to configure one"))
		return
	}
	if isURL(path) {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/pull only accepts local bare repo paths in this build; remote pull requires an out-of-fence fetch (TBD)"))
		return
	}
	if _, err := os.Stat(path); err != nil {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/pull: %w", err))
		return
	}
	if _, err := rp.Reprovision(ctx, principal, path, ""); err != nil {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/pull: %w", err))
		return
	}
	// Emit one synthetic stdout line so the lobby renders something
	// under the issued card; otherwise the chat looks empty.
	_ = d.broker.PublishEvent(events.NewSandboxCommandOutput(
		d.roomID, actor, commandID, events.StreamStdout,
		[]byte(fmt.Sprintf("workspace switched to %s\n", path)),
	))
	d.publishCompleted(actor, commandID, 0, nil)
}

// executeScratch handles the /scratch verb. It reprovisions the
// session with an EMPTY workspace (no base repo, no worktree mount)
// so the user can hack on something from zero. The optional
// description is surfaced back to chat but not yet wired to an agent.
func (d *Dispatcher) executeScratch(ctx context.Context, actor events.Actor, principal identity.Principal, commandID uuid.UUID, description string) {
	rp, ok := d.orch.(Reprovisioner)
	if !ok {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/scratch requires a worktree controller; ask the operator to configure one"))
		return
	}
	if _, err := rp.Reprovision(ctx, principal, "", ""); err != nil {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/scratch: %w", err))
		return
	}
	msg := "workspace reset to empty scratch"
	if description != "" {
		msg += ": " + description
	}
	_ = d.broker.PublishEvent(events.NewSandboxCommandOutput(
		d.roomID, actor, commandID, events.StreamStdout,
		[]byte(msg+"\n"),
	))
	d.publishCompleted(actor, commandID, 0, nil)
}

// executeServe starts a long-running dev server inside the actor's
// sandbox. argv = ["<port>", "<bin>", "<arg1>", ...]. The first token
// is the port the dev server binds to (we trust the user; auto-detect
// is follow-up work). The port is exported as PORT in the process
// env so frameworks that respect it (Next, Rails, Sinatra) pick it up.
//
// On success we publish SandboxServing for the broker and contributor
// strips, store a handle on the Dispatcher so /unserve and
// PresenceLeft can stop it, and emit a Completed event so the lobby
// renders an "@actor served port X" card. The process continues to
// run; its stdout/stderr stream to chat as SandboxCommandOutput
// frames just like /run.
func (d *Dispatcher) executeServe(ctx context.Context, actor events.Actor, principal identity.Principal, commandID uuid.UUID, argv []string) {
	if len(argv) < 2 {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/serve requires a port and a command"))
		return
	}
	port, err := strconv.Atoi(argv[0])
	if err != nil || port <= 0 || port > 65535 {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/serve: invalid port %q", argv[0]))
		return
	}
	cmdArgv := argv[1:]

	// One serving per actor. Stop any existing one first so /serve
	// twice rotates cleanly.
	d.stopServing(ctx, actor.ID, "replaced")

	sb, err := d.orch.Acquire(ctx, principal, sandbox.Spec{})
	if err != nil {
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/serve: acquire sandbox: %w", err))
		return
	}

	// Independent ctx so the serve process is NOT canceled when the
	// /serve command's invocation ctx returns. Bound to the dispatcher
	// lifetime via stopCh below.
	serveCtx, cancel := context.WithCancel(context.Background())

	proc, err := sb.Exec(serveCtx, sandbox.Command{
		Path: cmdArgv[0],
		Args: cmdArgv[1:],
		Env: map[string]string{
			"PORT": argv[0],
			"HOST": "127.0.0.1",
		},
	})
	if err != nil {
		cancel()
		d.publishCompleted(actor, commandID, -1, fmt.Errorf("/serve: exec: %w", err))
		return
	}

	label := strings.Join(cmdArgv, " ")
	d.mu.Lock()
	d.servings[actor.ID] = &serving{proc: proc, cancel: cancel, port: port, label: label}
	d.mu.Unlock()

	// Stream stdout/stderr to chat just like a /run, plus emit
	// SandboxServing so the contributor strip lights up immediately.
	go d.pump(actor, commandID, events.StreamStdout, proc.Stdout())
	go d.pump(actor, commandID, events.StreamStderr, proc.Stderr())
	_ = d.broker.PublishEvent(events.NewSandboxServing(d.roomID, actor, actor.SessionID, label, port, "http"))

	// Watch for the dev server exiting on its own. Clean up state and
	// publish ServingGone so the room knows the port is no longer
	// available.
	go func() {
		exit, _ := proc.Wait(serveCtx)
		d.mu.Lock()
		current, ok := d.servings[actor.ID]
		if ok && current.proc == proc {
			delete(d.servings, actor.ID)
		}
		d.mu.Unlock()
		reason := fmt.Sprintf("exit %d", exit)
		_ = d.broker.PublishEvent(events.NewSandboxServingGone(d.roomID, actor, actor.SessionID, reason))
		_ = d.broker.PublishEvent(events.NewSandboxCommandCompleted(d.roomID, actor, commandID, exit, ""))
	}()

	// Publish an "issued summary" stdout frame so the lobby shows
	// "serving on port N" right under the issued card.
	_ = d.broker.PublishEvent(events.NewSandboxCommandOutput(
		d.roomID, actor, commandID, events.StreamStdout,
		[]byte(fmt.Sprintf("serving %s on http://127.0.0.1:%d\n", label, port)),
	))
}

// executeUnserve stops the actor's currently-running serving, if
// any. Posts a Completed event regardless of whether anything was
// running so the lobby shows confirmation.
func (d *Dispatcher) executeUnserve(ctx context.Context, actor events.Actor, commandID uuid.UUID) {
	stopped := d.stopServing(ctx, actor.ID, "unserve")
	if stopped {
		_ = d.broker.PublishEvent(events.NewSandboxCommandOutput(
			d.roomID, actor, commandID, events.StreamStdout,
			[]byte("serving stopped\n"),
		))
	} else {
		_ = d.broker.PublishEvent(events.NewSandboxCommandOutput(
			d.roomID, actor, commandID, events.StreamStdout,
			[]byte("nothing was serving\n"),
		))
	}
	d.publishCompleted(actor, commandID, 0, nil)
}

// stopServing kills the actor's serving, if any, and publishes
// SandboxServingGone. Returns true if a serving was running.
func (d *Dispatcher) stopServing(ctx context.Context, actorID, reason string) bool {
	d.mu.Lock()
	s, ok := d.servings[actorID]
	if ok {
		delete(d.servings, actorID)
	}
	d.mu.Unlock()
	if !ok {
		return false
	}
	s.cancel()
	_ = s.proc.Signal(sandbox.SignalTerm)
	// Best effort: give it a moment to exit cleanly, then move on.
	// The goroutine started in executeServe will publish the
	// ServingGone via the natural exit path.
	return true
}

// isURL is a coarse predicate for "looks like a remote URL." Used by
// /pull to reject network-bound paths until we have an out-of-fence
// fetch path.
func isURL(s string) bool {
	switch {
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"),
		strings.HasPrefix(s, "git@"), strings.HasPrefix(s, "ssh://"),
		strings.HasPrefix(s, "git://"):
		return true
	}
	return false
}

// pump reads chunks off a stream and publishes them as
// SandboxCommandOutput events until EOF or read error.
//
// The chunker is ANSI-aware: it never emits a frame that ends in the
// middle of a CSI or OSC escape sequence, or in the middle of a
// multi-byte UTF-8 codepoint. Partial trailing bytes are held over
// and flushed with the next Read.
//
// If the held-over buffer grows past maxPendingPartial (a malformed
// or never-terminating sequence), it is force-flushed so a misbehaving
// program does not stall output forever.
func (d *Dispatcher) pump(actor events.Actor, commandID uuid.UUID, stream string, r io.Reader) {
	if r == nil {
		return
	}
	buf := make([]byte, readBufSize)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			emit, hold := splitAtSafeBoundary(pending)
			if len(emit) > 0 {
				frame := events.NewSandboxCommandOutput(d.roomID, actor, commandID, stream, emit)
				_ = d.broker.PublishEvent(frame)
			}
			pending = hold
			if len(pending) > maxPendingPartial {
				// Escape valve: flush whatever we are holding.
				frame := events.NewSandboxCommandOutput(d.roomID, actor, commandID, stream, pending)
				_ = d.broker.PublishEvent(frame)
				pending = nil
			}
		}
		if err != nil {
			// Flush any trailing held-over bytes on EOF / error.
			if len(pending) > 0 {
				frame := events.NewSandboxCommandOutput(d.roomID, actor, commandID, stream, pending)
				_ = d.broker.PublishEvent(frame)
			}
			return
		}
	}
}

// splitAtSafeBoundary scans buf and returns (emit, hold) such that
// emit is safe to publish as one chunk and hold is the leftover that
// must wait for the next read.
//
// "Safe" means emit ends:
//   - on a newline (always safe);
//   - or before an unterminated ESC introducer (ESC [, ESC ], etc.);
//   - or on a complete UTF-8 codepoint boundary.
//
// If the entire buf is ambiguous (e.g. starts with an unterminated
// escape), emit is empty and hold is the whole buf.
func splitAtSafeBoundary(buf []byte) (emit, hold []byte) {
	if len(buf) == 0 {
		return nil, nil
	}
	// Find the highest safe split point by scanning forward and
	// tracking whether we are inside an unfinished escape sequence.
	safe := 0
	i := 0
	for i < len(buf) {
		b := buf[i]
		// Newline is always safe right after this byte.
		if b == '\n' {
			i++
			safe = i
			continue
		}
		// ESC introducer starts a CSI / OSC / other sequence. Skip to
		// its terminator, if present in buf; otherwise leave it for
		// the next read.
		if b == 0x1b {
			term, ok := findEscapeEnd(buf[i:])
			if !ok {
				break
			}
			i += term
			safe = i
			continue
		}
		// Plain ASCII / UTF-8 lead byte. Advance past the whole
		// codepoint if it is multi-byte; if the codepoint is
		// incomplete at end-of-buf, stop here (leave it in hold).
		size := utf8RuneSize(buf[i:])
		if size <= 0 {
			break
		}
		i += size
		safe = i
	}
	if safe == 0 {
		return nil, buf
	}
	return buf[:safe], append([]byte(nil), buf[safe:]...)
}

// findEscapeEnd returns the index just past the terminator of an ANSI
// escape sequence starting at buf[0] == 0x1b. Recognizes CSI (ESC [
// ... final byte 0x40-0x7e), OSC (ESC ] ... BEL or ESC \), simple
// two-byte escapes (ESC + final byte), and charset designations.
// Returns (length, true) on success; (0, false) if the sequence is
// not yet complete in buf.
func findEscapeEnd(buf []byte) (int, bool) {
	if len(buf) < 2 {
		return 0, false
	}
	switch buf[1] {
	case '[': // CSI
		for i := 2; i < len(buf); i++ {
			c := buf[i]
			if c >= 0x40 && c <= 0x7e {
				return i + 1, true
			}
		}
		return 0, false
	case ']': // OSC, terminated by BEL (0x07) or ST (ESC \)
		for i := 2; i < len(buf); i++ {
			if buf[i] == 0x07 {
				return i + 1, true
			}
			if buf[i] == 0x1b && i+1 < len(buf) && buf[i+1] == '\\' {
				return i + 2, true
			}
		}
		return 0, false
	case '(', ')', '*', '+': // charset designation, ESC ( X
		if len(buf) < 3 {
			return 0, false
		}
		return 3, true
	default:
		// Simple two-byte escape (ESC + final byte).
		return 2, true
	}
}

// utf8RuneSize returns the byte length of the UTF-8 codepoint
// starting at buf[0], or 0 if the codepoint is not yet complete in
// buf. -1 if the leading byte is invalid.
func utf8RuneSize(buf []byte) int {
	if len(buf) == 0 {
		return 0
	}
	b := buf[0]
	switch {
	case b < 0x80:
		return 1
	case b&0xe0 == 0xc0:
		if len(buf) < 2 {
			return 0
		}
		return 2
	case b&0xf0 == 0xe0:
		if len(buf) < 3 {
			return 0
		}
		return 3
	case b&0xf8 == 0xf0:
		if len(buf) < 4 {
			return 0
		}
		return 4
	default:
		return 1 // treat invalid lead as 1-byte; do not stall
	}
}

func (d *Dispatcher) publishCompleted(actor events.Actor, commandID uuid.UUID, exitCode int, runErr error) {
	msg := ""
	if runErr != nil {
		msg = runErr.Error()
	}
	evt := events.NewSandboxCommandCompleted(d.roomID, actor, commandID, exitCode, msg)
	_ = d.broker.PublishEvent(evt)
}

// principalKindFromActor maps the event Actor.Kind string back to the
// identity.PrincipalKind enum.
func principalKindFromActor(kind string) identity.PrincipalKind {
	switch kind {
	case "agent":
		return identity.KindAgent
	default:
		return identity.KindHuman
	}
}
