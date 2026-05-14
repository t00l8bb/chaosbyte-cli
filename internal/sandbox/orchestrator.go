package sandbox

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/identity"
)

// Orchestrator owns the live set of per-user sandboxes for one team
// room. It maps Principal session IDs to Sandbox instances; when a
// user joins, Acquire spawns a new sandbox (if the user does not
// already have one); when a user leaves, Release destroys it.
//
// One Orchestrator per room. The vibespace daemon constructs one
// alongside each broker via the platform registry.
type Orchestrator struct {
	mu      sync.Mutex
	runtime Runtime
	defaultSpec Spec
	byUser  map[uuid.UUID]Sandbox
	closed  bool
}

// NewOrchestrator wires the runtime up and prepares to allocate
// sandboxes against the supplied default Spec. Callers can override
// the Spec per Acquire if a particular session needs a larger sandbox.
func NewOrchestrator(rt Runtime, defaultSpec Spec) *Orchestrator {
	return &Orchestrator{
		runtime:     rt,
		defaultSpec: defaultSpec,
		byUser:      map[uuid.UUID]Sandbox{},
	}
}

// Acquire returns the sandbox for the principal's current session,
// spawning one if none exists yet. Repeated calls with the same
// SessionID return the same Sandbox.
//
// Passing a Spec with zero fields uses the orchestrator's default;
// non-zero fields override.
func (o *Orchestrator) Acquire(ctx context.Context, p identity.Principal, spec Spec) (Sandbox, error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, ErrRuntimeClosed
	}
	if existing, ok := o.byUser[p.SessionID]; ok {
		o.mu.Unlock()
		return existing, nil
	}
	o.mu.Unlock()

	mergedSpec := mergeSpec(o.defaultSpec, spec)
	s, err := o.runtime.Spawn(ctx, mergedSpec)
	if err != nil {
		return nil, err
	}

	o.mu.Lock()
	if other, ok := o.byUser[p.SessionID]; ok {
		// A racing Acquire beat us. Destroy our duplicate and return
		// the winner.
		o.mu.Unlock()
		_ = s.Destroy(ctx)
		return other, nil
	}
	o.byUser[p.SessionID] = s
	o.mu.Unlock()
	return s, nil
}

// Release destroys the sandbox associated with the principal's
// session. Safe to call even if the principal had no sandbox; returns
// nil in that case.
func (o *Orchestrator) Release(ctx context.Context, p identity.Principal) error {
	o.mu.Lock()
	s, ok := o.byUser[p.SessionID]
	if ok {
		delete(o.byUser, p.SessionID)
	}
	o.mu.Unlock()
	if !ok {
		return nil
	}
	return s.Destroy(ctx)
}

// Lookup returns the sandbox associated with a session without
// spawning one. Returns nil if no sandbox is bound to this session.
func (o *Orchestrator) Lookup(sessionID uuid.UUID) Sandbox {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.byUser[sessionID]
}

// Close destroys every live sandbox and shuts the underlying Runtime
// down. The orchestrator becomes unusable afterward.
func (o *Orchestrator) Close(ctx context.Context) error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	live := make([]Sandbox, 0, len(o.byUser))
	for _, s := range o.byUser {
		live = append(live, s)
	}
	o.byUser = nil
	rt := o.runtime
	o.mu.Unlock()

	var firstErr error
	for _, s := range live {
		if err := s.Destroy(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if err := rt.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Live returns the number of currently-bound sandboxes. Used by the
// daemon's status surface.
func (o *Orchestrator) Live() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.byUser)
}

// mergeSpec returns base with any non-zero field from override
// applied.
func mergeSpec(base, override Spec) Spec {
	out := base
	if override.Image != "" {
		out.Image = override.Image
	}
	if override.CPU > 0 {
		out.CPU = override.CPU
	}
	if override.MemMB > 0 {
		out.MemMB = override.MemMB
	}
	if override.MaxLifetime > 0 {
		out.MaxLifetime = override.MaxLifetime
	}
	if len(override.Env) > 0 {
		if out.Env == nil {
			out.Env = map[string]string{}
		}
		for k, v := range override.Env {
			out.Env[k] = v
		}
	}
	if len(override.Mounts) > 0 {
		out.Mounts = append(out.Mounts, override.Mounts...)
	}
	return out
}

// Sanity check at compile time.
var _ = errors.New
