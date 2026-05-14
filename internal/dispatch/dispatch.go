package dispatch

import (
	"context"
	"errors"
	"fmt"
	"io"
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
}

// New returns a Dispatcher for the given room scope.
func New(broker Broker, orch Orchestrator, roomID string) *Dispatcher {
	return &Dispatcher{
		broker: broker,
		orch:   orch,
		roomID: roomID,
		stopCh: make(chan struct{}),
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
				// Release the actor's sandbox + worktree so they do
				// not leak. Best-effort; we ignore the error.
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

// chunkSize bounds the size of a single SandboxCommandOutput frame.
// Tuned for terminal output: large enough to amortize event overhead,
// small enough that a slow subscriber's buffer does not stall.
const chunkSize = 4 * 1024

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

// pump reads chunks off a stream and publishes them as
// SandboxCommandOutput events until EOF or read error.
func (d *Dispatcher) pump(actor events.Actor, commandID uuid.UUID, stream string, r io.Reader) {
	if r == nil {
		return
	}
	buf := make([]byte, chunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			frame := events.NewSandboxCommandOutput(d.roomID, actor, commandID, stream, buf[:n])
			_ = d.broker.PublishEvent(frame)
		}
		if err != nil {
			return
		}
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
