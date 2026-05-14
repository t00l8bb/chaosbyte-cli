// Package host is the OS-native sandbox Runtime. Each Spawn allocates
// a per-session temp directory and wraps every Exec call under the
// host kernel's process-isolation primitive: sandbox-exec on Darwin
// (Apple's TrustedBSD filter, the same one Safari renderers use) and
// bubblewrap on Linux (user-namespace + mount-namespace isolation, the
// same primitive Flatpak uses).
//
// The Runtime gives each session the following enforced fence:
//
//   - Writes are confined to the session's temp directory plus the
//     usual transient locations (/dev, /tmp, /private/var/folders).
//   - Reads cover the whole filesystem so toolchains and shared libs
//     resolve normally.
//   - Network is fully denied.
//   - On Linux, /proc is virtualized so the session cannot see other
//     processes on the host; on Darwin, mach-lookup is restricted.
//
// The fence is invisible to the user inside the sandbox: they see a
// normal shell that happens to fail with EACCES if it tries to escape.
//
// The runtime intentionally does NOT cap CPU, memory, or disk. Hard
// resource limits are the Firecracker backend's job; this backend is
// for trusted-user dev today.
package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bchayka/gitstatus/internal/sandbox"
)

// Runtime is the OS-native sandbox runtime.
type Runtime struct {
	root string

	mu       sync.Mutex
	closed   bool
	live     map[sandbox.ID]*Sandbox
	spawns   int64
	destroys int64
}

// New returns a Runtime that stages session directories under root.
// The root is created if missing; sessions live in root/<id>/ until
// destroyed.
func New(root string) (*Runtime, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("host sandbox: mkdir root: %w", err)
	}
	if err := ensureBackend(); err != nil {
		return nil, err
	}
	return &Runtime{
		root: root,
		live: map[sandbox.ID]*Sandbox{},
	}, nil
}

// Spawn allocates a new session directory and returns the Sandbox
// handle. The Sandbox is not "running" yet; only Exec actually starts
// a process under the fence.
func (r *Runtime) Spawn(_ context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, sandbox.ErrRuntimeClosed
	}
	id := sandbox.NewID()
	dir := filepath.Join(r.root, id.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("host sandbox: mkdir session: %w", err)
	}
	s := &Sandbox{
		id:        id,
		spec:      spec,
		dir:       dir,
		createdAt: time.Now(),
		runtime:   r,
	}
	r.live[id] = s
	atomic.AddInt64(&r.spawns, 1)
	return s, nil
}

// Close marks the runtime closed and destroys every live sandbox.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	for _, s := range r.live {
		_ = s.destroyLocked()
	}
	r.live = nil
	return nil
}

// Kind returns "host".
func (r *Runtime) Kind() string { return "host" }

// Stats returns cumulative spawn and destroy counts.
func (r *Runtime) Stats() (spawns, destroys int64) {
	return atomic.LoadInt64(&r.spawns), atomic.LoadInt64(&r.destroys)
}

// Sandbox is one live session keyed on its temp directory.
type Sandbox struct {
	id        sandbox.ID
	spec      sandbox.Spec
	dir       string
	createdAt time.Time
	runtime   *Runtime

	mu        sync.Mutex
	destroyed bool
	procs     []*Process
}

// ID returns the unique identifier assigned at Spawn.
func (s *Sandbox) ID() sandbox.ID { return s.id }

// CreatedAt returns the spawn time.
func (s *Sandbox) CreatedAt() time.Time { return s.createdAt }

// Dir returns the session's temp directory on the host. Callers
// outside the package may need it to drop files for the session to
// pick up (e.g. a worktree mount).
func (s *Sandbox) Dir() string { return s.dir }

// Destroy tears the sandbox down: kills every live Process, removes
// the session directory.
func (s *Sandbox) Destroy(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destroyLocked()
}

func (s *Sandbox) destroyLocked() error {
	if s.destroyed {
		return nil
	}
	s.destroyed = true
	for _, p := range s.procs {
		_ = p.Signal(sandbox.SignalKill)
	}
	_ = os.RemoveAll(s.dir)
	if s.runtime != nil {
		delete(s.runtime.live, s.id)
		atomic.AddInt64(&s.runtime.destroys, 1)
	}
	return nil
}

// Assert *Runtime satisfies sandbox.Runtime at compile time.
var _ sandbox.Runtime = (*Runtime)(nil)
var _ sandbox.Sandbox = (*Sandbox)(nil)
var _ = errors.New
