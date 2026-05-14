// Package plain is the fallback worktree Controller. It uses a real
// git worktree add against the base repo and lets the filesystem
// handle copies via standard tools (no CoW acceleration). Correct
// behavior on every Unix-y filesystem; slow at scale.
//
// Used as the unit-test backend (no FS-CoW prerequisite) and as the
// fallback when neither APFS (Mac) nor BTRFS (Linux) is available.
package plain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/bchayka/gitstatus/internal/worktree"
)

// Controller is the plain (no FS-CoW) implementation.
type Controller struct {
	mu       sync.Mutex
	root     string
	closed   bool
	live     map[worktree.ID]*tree
}

// New returns a Controller that stores worktrees under root. If root
// does not exist, it is created.
func New(root string) (*Controller, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("plain worktree: mkdir root: %w", err)
	}
	return &Controller{
		root: root,
		live: map[worktree.ID]*tree{},
	}, nil
}

// Provision runs `git worktree add` for the spec.
func (c *Controller) Provision(ctx context.Context, spec worktree.Spec) (worktree.Worktree, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, worktree.ErrControllerClosed
	}
	if spec.BaseRepo == "" {
		return nil, errors.New("plain worktree: spec.BaseRepo is required")
	}

	id := worktree.NewID()
	dest := spec.Dest
	if dest == "" {
		label := spec.Label
		if label == "" {
			label = "wt"
		}
		dest = filepath.Join(c.root, fmt.Sprintf("%s-%s", label, id.String()[:8]))
	}

	branch := spec.Branch
	args := []string{"worktree", "add"}
	if branch != "" {
		args = append(args, "--detach", dest, branch)
	} else {
		args = append(args, "--detach", dest)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = spec.BaseRepo
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("plain worktree: git worktree add: %w (output: %s)", err, string(out))
	}

	t := &tree{
		id:        id,
		path:      dest,
		branch:    branch,
		baseRepo:  spec.BaseRepo,
		createdAt: time.Now(),
		ctrl:      c,
	}
	c.live[id] = t
	return t, nil
}

// Close destroys every live worktree and marks the Controller closed.
func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	var firstErr error
	for _, t := range c.live {
		if err := t.destroyLocked(context.Background()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	c.live = nil
	return firstErr
}

// Kind returns "plain".
func (c *Controller) Kind() string { return "plain" }

// tree is the plain implementation of worktree.Worktree.
type tree struct {
	id        worktree.ID
	path      string
	branch    string
	baseRepo  string
	createdAt time.Time
	ctrl      *Controller

	mu        sync.Mutex
	destroyed bool
}

func (t *tree) ID() worktree.ID        { return t.id }
func (t *tree) Path() string           { return t.path }
func (t *tree) Branch() string         { return t.branch }
func (t *tree) CreatedAt() time.Time   { return t.createdAt }

func (t *tree) Destroy(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.destroyed {
		return nil
	}
	if err := t.destroyLocked(ctx); err != nil {
		return err
	}
	if t.ctrl != nil {
		t.ctrl.mu.Lock()
		delete(t.ctrl.live, t.id)
		t.ctrl.mu.Unlock()
	}
	return nil
}

func (t *tree) destroyLocked(ctx context.Context) error {
	if t.destroyed {
		return nil
	}
	t.destroyed = true

	// `git worktree remove --force` cleans both the FS and the
	// administrative .git/worktrees entry. We ignore errors from the
	// git command since the directory may already be gone.
	cmd := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", t.path)
	cmd.Dir = t.baseRepo
	_ = cmd.Run()

	// Best-effort rm in case git's worktree remove did not clean it.
	_ = os.RemoveAll(t.path)
	return nil
}

// Assert satisfies the interface at compile time.
var _ worktree.Controller = (*Controller)(nil)
var _ worktree.Worktree = (*tree)(nil)
