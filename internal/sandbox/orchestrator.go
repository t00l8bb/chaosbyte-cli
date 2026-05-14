package sandbox

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/worktree"
)

// Orchestrator owns the live set of per-user sandboxes for one team
// room. It maps Principal session IDs to Sandbox instances; when a
// user joins, Acquire spawns a new sandbox (if the user does not
// already have one); when a user leaves, Release destroys it.
//
// When a worktree.Controller is configured, every Acquire also
// provisions a fresh worktree clone from BaseRepo and mounts it into
// the sandbox at MountPath. Release destroys both.
//
// One Orchestrator per room. The vibespace daemon constructs one
// alongside each broker via the platform registry.
type Orchestrator struct {
	mu          sync.Mutex
	runtime     Runtime
	defaultSpec Spec
	worktrees   worktree.Controller
	baseRepo    string
	branch      string
	mountPath   string
	byUser      map[uuid.UUID]*session
	closed      bool
}

// session bundles a sandbox with its optional worktree so Release can
// tear both down together.
type session struct {
	sandbox  Sandbox
	worktree worktree.Worktree
}

// NewOrchestrator wires the runtime up and prepares to allocate
// sandboxes against the supplied default Spec. Callers can override
// the Spec per Acquire if a particular session needs a larger sandbox.
func NewOrchestrator(rt Runtime, defaultSpec Spec) *Orchestrator {
	return &Orchestrator{
		runtime:     rt,
		defaultSpec: defaultSpec,
		byUser:      map[uuid.UUID]*session{},
	}
}

// WithWorktrees configures the Orchestrator to provision a per-session
// worktree from baseRepo on every Acquire, mounted at mountPath inside
// the sandbox. branch is the git branch to check out (empty means
// HEAD). Returns the receiver for chaining.
//
// If baseRepo is empty the Orchestrator behaves as before: no worktree
// is provisioned and sandboxes get an empty session directory.
func (o *Orchestrator) WithWorktrees(ctrl worktree.Controller, baseRepo, branch, mountPath string) *Orchestrator {
	if mountPath == "" {
		mountPath = "/workspace"
	}
	o.worktrees = ctrl
	o.baseRepo = baseRepo
	o.branch = branch
	o.mountPath = mountPath
	return o
}

// Acquire returns the sandbox for the principal's current session,
// spawning one if none exists yet. Repeated calls with the same
// SessionID return the same Sandbox.
//
// When the Orchestrator has been configured with worktrees, a fresh
// worktree is also provisioned and bind-mounted into the sandbox at
// MountPath. The mount is appended to the sandbox Spec.Mounts so the
// host backend's profile/bwrap args include it.
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
		return existing.sandbox, nil
	}
	o.mu.Unlock()

	mergedSpec := mergeSpec(o.defaultSpec, spec)

	// If a worktree controller is configured, provision the user's
	// workspace first so the host backend sees its path in
	// mergedSpec.Mounts when it builds the fence.
	var wt worktree.Worktree
	if o.worktrees != nil && o.baseRepo != "" {
		var err error
		wt, err = o.worktrees.Provision(ctx, worktree.Spec{
			BaseRepo: o.baseRepo,
			Branch:   o.branch,
			Label:    safeLabel(p.DisplayName),
		})
		if err != nil {
			return nil, err
		}
		mergedSpec.Mounts = append(mergedSpec.Mounts, Mount{
			HostPath:    wt.Path(),
			SandboxPath: o.mountPath,
			ReadOnly:    false,
		})
	}

	s, err := o.runtime.Spawn(ctx, mergedSpec)
	if err != nil {
		if wt != nil {
			_ = wt.Destroy(ctx)
		}
		return nil, err
	}

	o.mu.Lock()
	if other, ok := o.byUser[p.SessionID]; ok {
		// A racing Acquire beat us. Tear down our duplicates and
		// return the winner.
		o.mu.Unlock()
		_ = s.Destroy(ctx)
		if wt != nil {
			_ = wt.Destroy(ctx)
		}
		return other.sandbox, nil
	}
	o.byUser[p.SessionID] = &session{sandbox: s, worktree: wt}
	o.mu.Unlock()
	return s, nil
}

// Reprovision tears down the principal's current sandbox + worktree
// (if any) and provisions fresh ones rooted at baseRepo and branch.
// Used by the /pull verb: the user supplies a different repo and
// gets a clean sandbox bound to it.
//
// If baseRepo is empty the session ends up with an empty sandbox and
// no worktree mount, matching the /scratch verb's eventual shape.
func (o *Orchestrator) Reprovision(ctx context.Context, p identity.Principal, baseRepo, branch string) (Sandbox, error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil, ErrRuntimeClosed
	}
	if o.worktrees == nil {
		o.mu.Unlock()
		return nil, errors.New("orchestrator: no worktree controller configured; cannot reprovision")
	}
	prev := o.byUser[p.SessionID]
	delete(o.byUser, p.SessionID)
	ctrl := o.worktrees
	mountPath := o.mountPath
	mergedSpec := o.defaultSpec
	o.mu.Unlock()

	// Tear down the previous session first so we never run two
	// sandboxes for one user. Errors are best-effort.
	if prev != nil {
		_ = prev.sandbox.Destroy(ctx)
		if prev.worktree != nil {
			_ = prev.worktree.Destroy(ctx)
		}
	}

	var wt worktree.Worktree
	if baseRepo != "" {
		w, err := ctrl.Provision(ctx, worktree.Spec{
			BaseRepo: baseRepo,
			Branch:   branch,
			Label:    safeLabel(p.DisplayName),
		})
		if err != nil {
			return nil, err
		}
		wt = w
		mergedSpec.Mounts = append([]Mount{}, mergedSpec.Mounts...)
		mergedSpec.Mounts = append(mergedSpec.Mounts, Mount{
			HostPath:    w.Path(),
			SandboxPath: mountPath,
			ReadOnly:    false,
		})
	}

	s, err := o.runtime.Spawn(ctx, mergedSpec)
	if err != nil {
		if wt != nil {
			_ = wt.Destroy(ctx)
		}
		return nil, err
	}

	o.mu.Lock()
	o.byUser[p.SessionID] = &session{sandbox: s, worktree: wt}
	o.mu.Unlock()
	return s, nil
}

// Release destroys the sandbox (and any attached worktree) associated
// with the principal's session. Safe to call even if the principal
// had no sandbox; returns nil in that case.
func (o *Orchestrator) Release(ctx context.Context, p identity.Principal) error {
	o.mu.Lock()
	sess, ok := o.byUser[p.SessionID]
	if ok {
		delete(o.byUser, p.SessionID)
	}
	o.mu.Unlock()
	if !ok {
		return nil
	}
	err := sess.sandbox.Destroy(ctx)
	if sess.worktree != nil {
		if werr := sess.worktree.Destroy(ctx); werr != nil && err == nil {
			err = werr
		}
	}
	return err
}

// Lookup returns the sandbox associated with a session without
// spawning one. Returns nil if no sandbox is bound to this session.
func (o *Orchestrator) Lookup(sessionID uuid.UUID) Sandbox {
	o.mu.Lock()
	defer o.mu.Unlock()
	if sess, ok := o.byUser[sessionID]; ok {
		return sess.sandbox
	}
	return nil
}

// Close destroys every live sandbox and worktree and shuts the
// underlying Runtime down. The orchestrator becomes unusable
// afterward.
func (o *Orchestrator) Close(ctx context.Context) error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	live := make([]*session, 0, len(o.byUser))
	for _, s := range o.byUser {
		live = append(live, s)
	}
	o.byUser = nil
	rt := o.runtime
	ctrl := o.worktrees
	o.mu.Unlock()

	var firstErr error
	for _, sess := range live {
		if err := sess.sandbox.Destroy(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
		if sess.worktree != nil {
			if err := sess.worktree.Destroy(ctx); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if err := rt.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if ctrl != nil {
		if err := ctrl.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// safeLabel returns a filesystem-safe label derived from a display
// name. Used for worktree directory naming so the path is human
// navigable.
func safeLabel(name string) string {
	if name == "" {
		return "session"
	}
	out := make([]rune, 0, len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "session"
	}
	return string(out)
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
