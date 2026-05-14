// Package worktree is the per-user filesystem layer that sits next to
// each sandbox. When a user joins a room, the orchestrator asks the
// Controller for a fresh Worktree off the room's base repo; the
// Worktree lives in a copy-on-write clone of the base so the user can
// edit without touching anyone else's view. On leave, Destroy reclaims
// the storage.
//
// The package defines two interfaces:
//
//   - Controller: a backend that knows how to provision and destroy
//     worktrees on the host. Implementations: apfs (Mac, native
//     clonefile), btrfs (Linux, subvolume snapshot), plain (fallback
//     using cp -r; correct but slow; useful only for tests and
//     fallbacks).
//   - Worktree: a single live working copy. Path is where files
//     live, Branch identifies the underlying git branch, Destroy
//     cleans it up.
//
// Backends are selected by Detect(base) at boot: try apfs on Mac,
// btrfs on Linux, plain everywhere else. The orchestrator code is
// identical across backends.
package worktree

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ID is the unique identifier for a worktree.
type ID uuid.UUID

func (id ID) String() string { return uuid.UUID(id).String() }

// Worktree is one live per-user working copy.
type Worktree interface {
	// ID returns the unique identifier.
	ID() ID

	// Path returns the absolute filesystem path of the worktree on
	// the host. The orchestrator passes this into sandbox.Spec.Mounts
	// so the sandbox sees the files at a known location.
	Path() string

	// Branch is the git branch the worktree is checked out on. For
	// Phase 2 this is the same as the room's default branch; per-user
	// branches land in Phase 3.
	Branch() string

	// CreatedAt is the wall-clock time the worktree was provisioned.
	CreatedAt() time.Time

	// Destroy releases the storage. Idempotent.
	Destroy(ctx context.Context) error
}

// Controller is the backend that provisions and destroys worktrees.
type Controller interface {
	// Provision clones the base repo for a new user session. Returns
	// the live Worktree handle; the caller is responsible for calling
	// Destroy when the session ends.
	Provision(ctx context.Context, spec Spec) (Worktree, error)

	// Close releases backend-wide resources (lock files, control
	// sockets). After Close, Provision returns ErrControllerClosed.
	Close() error

	// Kind identifies the backend ("apfs", "btrfs", "plain") for logs.
	Kind() string
}

// Spec describes a worktree the caller wants provisioned.
type Spec struct {
	// BaseRepo is the absolute path of the bare repository the
	// worktree clones from. For Phase 2 v0.1, monobyte hosts one bare
	// repo per team room.
	BaseRepo string

	// Branch is the branch to check out. Empty means HEAD.
	Branch string

	// Dest is the optional destination path. If empty the controller
	// picks a path under the controller's pool directory.
	Dest string

	// Label is a free-form tag (typically the principal's display
	// name) the controller embeds in the path for human navigation.
	Label string
}

// ErrControllerClosed is returned by Provision after the Controller
// has been closed.
var ErrControllerClosed = errors.New("worktree: controller is closed")

// ErrWorktreeDestroyed is returned when accessing a destroyed worktree.
var ErrWorktreeDestroyed = errors.New("worktree: worktree is destroyed")

// NewID returns a fresh worktree identifier.
func NewID() ID { return ID(uuid.New()) }
