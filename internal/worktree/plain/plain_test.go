package plain_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bchayka/gitstatus/internal/worktree"
	"github.com/bchayka/gitstatus/internal/worktree/plain"
)

// setupBareRepo creates a bare git repo seeded with a single commit
// and returns its absolute path. Worktree.Provision targets this repo.
func setupBareRepo(t *testing.T) (bare string) {
	t.Helper()

	// 1. Build a working repo with a single commit.
	work := t.TempDir()
	mustRun(t, work, "git", "init", "-q", "-b", "main")
	mustRun(t, work, "git", "config", "user.name", "test")
	mustRun(t, work, "git", "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, work, "git", "add", "README.md")
	mustRun(t, work, "git", "commit", "-q", "-m", "initial")

	// 2. Clone --bare into a fresh dir.
	bare = filepath.Join(t.TempDir(), "repo.git")
	mustRun(t, "", "git", "clone", "--bare", work, bare)
	return bare
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, string(out))
	}
}

func TestPlainProvisionAndDestroy(t *testing.T) {
	bare := setupBareRepo(t)
	root := t.TempDir()
	c, err := plain.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	wt, err := c.Provision(context.Background(), worktree.Spec{
		BaseRepo: bare,
		Label:    "alice",
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	// Worktree path exists and contains the README.
	if _, err := os.Stat(filepath.Join(wt.Path(), "README.md")); err != nil {
		t.Fatalf("worktree should contain README.md: %v", err)
	}

	// Destroy removes it.
	if err := wt.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(wt.Path()); !os.IsNotExist(err) {
		t.Errorf("path should be gone after Destroy, got err=%v", err)
	}
}

func TestPlainProvisionEmptyBaseRepo(t *testing.T) {
	c, err := plain.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, err = c.Provision(context.Background(), worktree.Spec{Label: "alice"})
	if err == nil {
		t.Error("Provision should reject empty BaseRepo")
	}
}

func TestPlainDestroyIsIdempotent(t *testing.T) {
	bare := setupBareRepo(t)
	c, _ := plain.New(t.TempDir())
	defer c.Close()
	wt, _ := c.Provision(context.Background(), worktree.Spec{BaseRepo: bare, Label: "bob"})
	if err := wt.Destroy(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Calling again should be a no-op, not an error.
	if err := wt.Destroy(context.Background()); err != nil {
		t.Errorf("second Destroy: %v", err)
	}
}

func TestPlainCloseTearsDownAll(t *testing.T) {
	bare := setupBareRepo(t)
	c, _ := plain.New(t.TempDir())
	for _, label := range []string{"alice", "bob", "cleo"} {
		_, err := c.Provision(context.Background(), worktree.Spec{BaseRepo: bare, Label: label})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	// Post-close Provision should error.
	_, err := c.Provision(context.Background(), worktree.Spec{BaseRepo: bare, Label: "late"})
	if err == nil {
		t.Error("Provision after Close should error")
	}
}

func TestPlainKind(t *testing.T) {
	c, _ := plain.New(t.TempDir())
	defer c.Close()
	if c.Kind() != "plain" {
		t.Errorf("Kind = %q, want plain", c.Kind())
	}
}
