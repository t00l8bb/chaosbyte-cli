// Package sandbox is the per-user isolated environment in which dev
// servers, agent tool calls, and the worktree all live for the duration
// of a session. Each user joining a monobyte room receives their own
// Sandbox; on leave the Sandbox is destroyed.
//
// The package defines two interfaces:
//
//   - Runtime: a backend factory that knows how to spawn and destroy
//     sandboxes on the host. Phase 2 ships two implementations:
//     mock (in-process, for tests) and firecracker (Linux microVMs,
//     production).
//   - Sandbox: a single live instance. Exec runs commands inside it,
//     PTY returns a pseudo-terminal, Wait blocks until the process
//     ends, Destroy tears it down.
//
// The Phase 1 design called for Firecracker as the only production
// backend. The mock implementation lives in the codebase permanently
// for unit tests; it is not a v0.1 development shortcut.
package sandbox

import (
	"context"
	"io"
	"time"

	"github.com/google/uuid"
)

// ID is the unique identifier for a running sandbox. Assigned by the
// Runtime on Spawn. The same ID flows into events.SandboxStarted /
// SandboxStopped on the bus.
type ID uuid.UUID

func (id ID) String() string { return uuid.UUID(id).String() }

// Spec describes the sandbox a caller wants. The Runtime picks a
// concrete instance whose fields match.
type Spec struct {
	// Image is the rootfs identifier. For firecracker this is a
	// snapshot name from the warm pool; for mock it is a free-form
	// label.
	Image string

	// CPU is the requested vCPU count. The runtime may round up if the
	// pool only carries fixed sizes.
	CPU int

	// MemMB is the requested memory in megabytes.
	MemMB int

	// Env is the environment variables to set inside the sandbox.
	Env map[string]string

	// Mounts is the set of host->sandbox bind mounts. For firecracker
	// these become virtio-fs mounts; for mock they are no-ops.
	Mounts []Mount

	// MaxLifetime caps how long the sandbox may live before automatic
	// teardown. Zero means no cap.
	MaxLifetime time.Duration
}

// Mount is a host-to-sandbox path binding.
type Mount struct {
	HostPath    string
	SandboxPath string
	ReadOnly    bool
}

// Runtime is the backend that spawns sandboxes. The vibespace daemon
// holds one Runtime per host; each connecting user gets their own
// Sandbox from it.
type Runtime interface {
	// Spawn creates a new sandbox matching the Spec. Returns the
	// sandbox handle; the caller is responsible for calling Destroy
	// when the session ends.
	Spawn(ctx context.Context, spec Spec) (Sandbox, error)

	// Close releases any pool resources the runtime holds (warm
	// snapshots, control sockets). After Close, Spawn returns
	// ErrRuntimeClosed.
	Close() error

	// Kind identifies the backend ("mock", "firecracker") for logs
	// and metrics.
	Kind() string
}

// Sandbox is one live isolated environment.
type Sandbox interface {
	// ID returns the unique identifier assigned at Spawn.
	ID() ID

	// Exec runs a command inside the sandbox and returns a Process
	// handle. The caller waits on the process or asks for its PTY.
	Exec(ctx context.Context, cmd Command) (Process, error)

	// Destroy tears the sandbox down. Idempotent; safe to call after
	// the sandbox has already exited.
	Destroy(ctx context.Context) error

	// CreatedAt is the wall-clock time the sandbox was spawned.
	CreatedAt() time.Time
}

// Command describes a single command to run inside a sandbox.
type Command struct {
	// Path is the binary or shell builtin to invoke.
	Path string

	// Args is the argument list (excluding the binary itself).
	Args []string

	// Env layers on top of the Sandbox's Spec.Env.
	Env map[string]string

	// WorkingDir is the directory inside the sandbox to cd into
	// before running.
	WorkingDir string

	// TTY requests an interactive PTY. When true, the returned Process
	// exposes PTY().
	TTY bool
}

// Process is a running command inside a Sandbox.
type Process interface {
	// Stdin / Stdout / Stderr are the I/O streams for non-TTY commands.
	// All three are valid until Wait returns.
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser

	// PTY returns the pseudo-terminal master, if Command.TTY was true.
	// Returns nil otherwise.
	PTY() io.ReadWriteCloser

	// Wait blocks until the process exits and returns its exit code.
	// Cancelling the context terminates the process.
	Wait(ctx context.Context) (exitCode int, err error)

	// Signal sends a signal to the process. Use Kill to terminate.
	Signal(sig Signal) error

	// PID returns the process id inside the sandbox (informational).
	PID() int
}

// Signal is a sandbox-portable signal name. Backends map these to
// their native signal types.
type Signal string

const (
	SignalTerm Signal = "TERM"
	SignalKill Signal = "KILL"
	SignalInt  Signal = "INT"
	SignalHUP  Signal = "HUP"
)

// NewID returns a fresh sandbox identifier.
func NewID() ID { return ID(uuid.New()) }
