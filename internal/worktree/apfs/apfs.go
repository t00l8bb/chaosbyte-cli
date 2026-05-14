//go:build darwin

// Package apfs is the macOS-native worktree Controller. It uses the
// APFS clonefile() syscall via golang.org/x/sys/unix to produce
// copy-on-write directory copies in O(1). The clone shares the
// underlying blocks with the source until either side writes; only
// then do new blocks materialize.
//
// Build tag: darwin. Lima Linux guests pick btrfs or plain instead.
package apfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bchayka/gitstatus/internal/worktree"
)

// Controller is the APFS-backed worktree controller.
type Controller struct {
	mu     sync.Mutex
	root   string
	closed bool
	live   map[worktree.ID]*tree
}

// New returns a Controller storing worktrees under root. The root
// must live on an APFS volume; the package does not detect this at
// construction time. Callers should fall back to plain on non-APFS.
func New(root string) (*Controller, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("apfs worktree: mkdir root: %w", err)
	}
	return &Controller{
		root: root,
		live: map[worktree.ID]*tree{},
	}, nil
}

// Provision clones the base repo's checkout tree via APFS clonefile.
// The source must already be a git worktree checkout, not a bare repo;
// the controller pre-stages a checkout under the controller root.
func (c *Controller) Provision(ctx context.Context, spec worktree.Spec) (worktree.Worktree, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, worktree.ErrControllerClosed
	}
	if spec.BaseRepo == "" {
		return nil, errors.New("apfs worktree: spec.BaseRepo is required")
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

	source, err := c.ensureCheckout(ctx, spec.BaseRepo, spec.Branch)
	if err != nil {
		return nil, err
	}

	// CLONE_NOFOLLOW preserves symlinks; the default recursive
	// behavior on a directory source clones the whole tree.
	if err := unix.Clonefile(source, dest, unix.CLONE_NOFOLLOW); err != nil {
		return nil, fmt.Errorf("apfs worktree: clonefile: %w", err)
	}

	t := &tree{
		id:        id,
		path:      dest,
		branch:    spec.Branch,
		createdAt: time.Now(),
		ctrl:      c,
	}
	c.live[id] = t
	return t, nil
}

// ensureCheckout creates (once per BaseRepo+Branch) a pristine
// checkout under the controller's root. Subsequent Provisions for the
// same source share blocks with that checkout via clonefile.
func (c *Controller) ensureCheckout(ctx context.Context, bareRepo, branch string) (string, error) {
	// One staging dir per (repo, branch).
	stageName := branchSafe(branch)
	if stageName == "" {
		stageName = "HEAD"
	}
	stage := filepath.Join(c.root, ".stage", filepath.Base(bareRepo)+"."+stageName)
	if _, err := os.Stat(stage); err == nil {
		return stage, nil
	}
	if err := os.MkdirAll(filepath.Dir(stage), 0o755); err != nil {
		return "", err
	}
	args := []string{"clone", "--no-hardlinks"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, bareRepo, stage)
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("apfs worktree: clone stage: %w (output: %s)", err, string(out))
	}
	return stage, nil
}

// branchSafe returns a filename-safe form of branch (or empty string).
func branchSafe(branch string) string {
	out := make([]rune, 0, len(branch))
	for _, r := range branch {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// Close destroys every live worktree.
func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	for _, t := range c.live {
		_ = t.destroyLocked(context.Background())
	}
	c.live = nil
	return nil
}

// Kind returns "apfs".
func (c *Controller) Kind() string { return "apfs" }

type tree struct {
	id        worktree.ID
	path      string
	branch    string
	createdAt time.Time
	ctrl      *Controller

	mu        sync.Mutex
	destroyed bool
}

func (t *tree) ID() worktree.ID      { return t.id }
func (t *tree) Path() string         { return t.path }
func (t *tree) Branch() string       { return t.branch }
func (t *tree) CreatedAt() time.Time { return t.createdAt }

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

func (t *tree) destroyLocked(_ context.Context) error {
	if t.destroyed {
		return nil
	}
	t.destroyed = true
	return os.RemoveAll(t.path)
}

var _ worktree.Controller = (*Controller)(nil)
var _ worktree.Worktree = (*tree)(nil)
