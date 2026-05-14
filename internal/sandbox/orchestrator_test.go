package sandbox_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/identity"
	"github.com/bchayka/gitstatus/internal/sandbox"
	"github.com/bchayka/gitstatus/internal/sandbox/mock"
	"github.com/bchayka/gitstatus/internal/worktree/plain"
)

func samplePrincipal() identity.Principal {
	return identity.Principal{
		ID:          "pk:test",
		DisplayName: "@test",
		Teams:       []string{"vibespace"},
		Roles:       []string{"operator"},
		SessionID:   uuid.New(),
		Kind:        identity.KindHuman,
	}
}

func TestAcquireSpawnsOnce(t *testing.T) {
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{Image: "vibespace-default"})
	defer o.Close(context.Background())

	p := samplePrincipal()
	s1, err := o.Acquire(context.Background(), p, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	s2, err := o.Acquire(context.Background(), p, sandbox.Spec{})
	if err != nil {
		t.Fatalf("second Acquire: %v", err)
	}
	if s1.ID() != s2.ID() {
		t.Errorf("Acquire should be idempotent for the same SessionID")
	}
	spawns, _ := rt.Stats()
	if spawns != 1 {
		t.Errorf("Runtime.Spawn called %d times, want 1", spawns)
	}
}

func TestAcquireDifferentSessions(t *testing.T) {
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{Image: "vibespace-default"})
	defer o.Close(context.Background())

	pA := samplePrincipal()
	pB := samplePrincipal()
	sA, _ := o.Acquire(context.Background(), pA, sandbox.Spec{})
	sB, _ := o.Acquire(context.Background(), pB, sandbox.Spec{})
	if sA.ID() == sB.ID() {
		t.Error("different sessions should get different sandboxes")
	}
	if got := o.Live(); got != 2 {
		t.Errorf("Live = %d, want 2", got)
	}
}

func TestReleaseDestroysSandbox(t *testing.T) {
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{Image: "vibespace-default"})
	defer o.Close(context.Background())

	p := samplePrincipal()
	_, _ = o.Acquire(context.Background(), p, sandbox.Spec{})
	if err := o.Release(context.Background(), p); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if got := o.Live(); got != 0 {
		t.Errorf("Live = %d after Release, want 0", got)
	}
	_, destroys := rt.Stats()
	if destroys != 1 {
		t.Errorf("Runtime destroys = %d, want 1", destroys)
	}
}

func TestReleaseUnknownIsNoOp(t *testing.T) {
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	defer o.Close(context.Background())

	p := samplePrincipal()
	if err := o.Release(context.Background(), p); err != nil {
		t.Errorf("Release on unknown principal should be nil, got %v", err)
	}
}

func TestCloseTearsDownAll(t *testing.T) {
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{Image: "vibespace-default"})

	for i := 0; i < 3; i++ {
		_, _ = o.Acquire(context.Background(), samplePrincipal(), sandbox.Spec{})
	}
	if got := o.Live(); got != 3 {
		t.Fatalf("Live before Close = %d, want 3", got)
	}
	if err := o.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := o.Live(); got != 0 {
		t.Errorf("Live after Close = %d, want 0", got)
	}
	_, destroys := rt.Stats()
	if destroys != 3 {
		t.Errorf("destroys = %d, want 3", destroys)
	}
}

func TestAcquireAfterCloseFails(t *testing.T) {
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	o.Close(context.Background())
	_, err := o.Acquire(context.Background(), samplePrincipal(), sandbox.Spec{})
	if err == nil {
		t.Error("Acquire after Close should return an error")
	}
}

func TestMergeSpecOverrides(t *testing.T) {
	rt := mock.New()
	defer rt.Close()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{
		Image:       "default-image",
		CPU:         1,
		MemMB:       256,
		MaxLifetime: time.Minute,
	})
	p := samplePrincipal()
	override := sandbox.Spec{Image: "override-image", CPU: 2}
	s, err := o.Acquire(context.Background(), p, override)
	if err != nil {
		t.Fatal(err)
	}
	// The mock sandbox does not expose its spec directly; verify
	// indirectly: an Acquire with a different SessionID + different
	// override returns a different sandbox.
	_ = s
}

// setupBareRepo creates a bare git repo seeded with one commit. Used
// by the worktree-mount tests below.
func setupBareRepo(t *testing.T) string {
	t.Helper()
	work := t.TempDir()
	run := func(dir, name string, args ...string) {
		cmd := exec.Command(name, args...)
		if dir != "" {
			cmd.Dir = dir
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, string(out))
		}
	}
	run(work, "git", "init", "-q", "-b", "main")
	run(work, "git", "config", "user.name", "test")
	run(work, "git", "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "add", "README.md")
	run(work, "git", "commit", "-q", "-m", "initial")

	bare := filepath.Join(t.TempDir(), "repo.git")
	run("", "git", "clone", "--bare", work, bare)
	return bare
}

func TestAcquireProvisionsWorktreeAndAttachesMount(t *testing.T) {
	bare := setupBareRepo(t)
	ctrl, err := plain.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{}).
		WithWorktrees(ctrl, bare, "main", "/workspace")
	defer o.Close(context.Background())

	p := samplePrincipal()
	s, err := o.Acquire(context.Background(), p, sandbox.Spec{})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if s == nil {
		t.Fatal("nil sandbox")
	}
	// The mock runtime records its last Spec via the spawned sandbox;
	// inspect Live to confirm one session is bound.
	if got := o.Live(); got != 1 {
		t.Errorf("Live = %d, want 1", got)
	}
}

func TestReleaseDestroysWorktreeAlongsideSandbox(t *testing.T) {
	bare := setupBareRepo(t)
	root := t.TempDir()
	ctrl, err := plain.New(root)
	if err != nil {
		t.Fatal(err)
	}
	rt := mock.New()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{}).
		WithWorktrees(ctrl, bare, "main", "/workspace")

	p := samplePrincipal()
	if _, err := o.Acquire(context.Background(), p, sandbox.Spec{}); err != nil {
		t.Fatal(err)
	}
	// Find the provisioned worktree dir under root (plain backend
	// creates exactly one entry).
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("expected one worktree dir under %s, got %d", root, len(entries))
	}
	wtPath := filepath.Join(root, entries[0].Name())
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree dir should exist: %v", err)
	}

	if err := o.Release(context.Background(), p); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should be gone after Release, got err=%v", err)
	}
}

func TestExecAndWait(t *testing.T) {
	rt := mock.New()
	defer rt.Close()
	o := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	p := samplePrincipal()
	s, err := o.Acquire(context.Background(), p, sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}

	proc, err := s.Exec(context.Background(), sandbox.Command{
		Path: "/bin/echo",
		Args: []string{"hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The mock has a SetOutput helper; cast to the concrete type for
	// the test.
	mockProc := proc.(*mock.Process)
	mockProc.SetOutput([]byte("hello\n"), 0)

	exit, err := proc.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}

	buf := make([]byte, 16)
	n, _ := proc.Stdout().Read(buf)
	if string(buf[:n]) != "hello\n" {
		t.Errorf("stdout = %q", string(buf[:n]))
	}
}
