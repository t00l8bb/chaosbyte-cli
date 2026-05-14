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
			chat, isChat := evt.(*events.ChatPosted)
			if !isChat {
				continue
			}
			parsed, err := Parse(chat.Body)
			if err != nil {
				// Not a command, or malformed. Either way, no dispatch.
				continue
			}
			d.wg.Add(1)
			go func() {
				defer d.wg.Done()
				d.execute(ctx, chat, parsed)
			}()
		}
	}
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
	issued := events.NewSandboxCommandIssued(d.roomID, actor, commandID, actor.SessionID, cmd.Argv)
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

	errMsg := ""
	if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
		errMsg = waitErr.Error()
	}
	d.publishCompleted(actor, commandID, exitCode, nil)
	if errMsg != "" {
		// Surface a second completed event with the error for
		// observability; the first one already carried the exit code.
		_ = errMsg
	}
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
