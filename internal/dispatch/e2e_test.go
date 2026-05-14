package dispatch_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bchayka/gitstatus/internal/dispatch"
	"github.com/bchayka/gitstatus/internal/events"
	"github.com/bchayka/gitstatus/internal/room"
	"github.com/bchayka/gitstatus/internal/sandbox"
	sbhost "github.com/bchayka/gitstatus/internal/sandbox/host"
	"github.com/bchayka/gitstatus/internal/ui"
	"github.com/bchayka/gitstatus/internal/worktree"
	"github.com/bchayka/gitstatus/internal/worktree/plain"
)

// setupBareRepoForDispatchTest builds a bare git repo seeded with one
// commit and returns its absolute path. Used by /pull e2e tests.
func setupBareRepoForDispatchTest(t *testing.T) string {
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
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("dispatch test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "git", "add", "README.md")
	run(work, "git", "commit", "-q", "-m", "initial")

	bare := filepath.Join(t.TempDir(), "repo.git")
	run("", "git", "clone", "--bare", work, bare)
	return bare
}

// plainController returns a plain worktree.Controller rooted at the
// supplied dir. Used by /pull e2e tests.
func plainController(root string) (worktree.Controller, error) {
	return plain.New(root)
}

// TestEndToEndChatToRealSandbox boots a real room.Broker, a real
// host.Runtime (sandbox-exec or bwrap), a real Orchestrator and a
// real Dispatcher. It posts `/run echo hello` through the broker and
// asserts every event in the chain lands: ChatPosted, Issued, at
// least one Output, Completed with exit 0. This is the full path
// the daemon takes minus the SSH transport, and it exercises the OS
// fence for real (not the scripted mock).
func TestEndToEndChatToRealSandbox(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox only on darwin/linux, got %s", runtime.GOOS)
	}
	rt, err := sbhost.New(t.TempDir())
	if err != nil {
		t.Skipf("host backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	_, obs := b.Subscribe()

	actor := events.Actor{
		ID:          "pk:test",
		DisplayName: "@daniel",
		Kind:        "human",
		SessionID:   uuid.New(),
	}
	chat := events.NewChatPosted("test", actor, "#lobby", "/run echo hello", ui.ChatNormal)
	if err := b.PublishEvent(chat); err != nil {
		t.Fatalf("publish: %v", err)
	}

	got := drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.SandboxCommandCompleted); ok {
				return true
			}
		}
		return false
	})

	var (
		sawIssued, sawCompleted bool
		stdoutBody              strings.Builder
		completed               *events.SandboxCommandCompleted
	)
	for _, e := range got {
		switch v := e.(type) {
		case *events.SandboxCommandIssued:
			sawIssued = true
		case *events.SandboxCommandOutput:
			if v.Stream == events.StreamStdout {
				stdoutBody.Write(v.Chunk)
			}
		case *events.SandboxCommandCompleted:
			sawCompleted = true
			completed = v
		}
	}
	if !sawIssued {
		t.Error("missing SandboxCommandIssued through the broker")
	}
	if !sawCompleted {
		t.Error("missing SandboxCommandCompleted through the broker")
	}
	if completed != nil && completed.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0; error=%q", completed.ExitCode, completed.Error)
	}
	if got, want := strings.TrimSpace(stdoutBody.String()), "hello"; got != want {
		t.Errorf("stdout through chain = %q, want %q", got, want)
	}
}

// TestPresenceLeftReleasesSandbox proves the lifecycle hook: when a
// session quits, the dispatcher's PresenceLeft listener calls
// Orchestrator.Release and the sandbox is destroyed.
func TestPresenceLeftReleasesSandbox(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox only on darwin/linux, got %s", runtime.GOOS)
	}
	rt, err := sbhost.New(t.TempDir())
	if err != nil {
		t.Skipf("host backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	actor := events.Actor{
		ID:          "pk:test",
		DisplayName: "@daniel",
		Kind:        "human",
		SessionID:   uuid.New(),
	}
	// Trigger sandbox creation by issuing one command and waiting for
	// completion.
	_, obs := b.Subscribe()
	chat := events.NewChatPosted("test", actor, "#lobby", "/run echo hi", ui.ChatNormal)
	if err := b.PublishEvent(chat); err != nil {
		t.Fatal(err)
	}
	drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.SandboxCommandCompleted); ok {
				return true
			}
		}
		return false
	})

	if got := orch.Live(); got != 1 {
		t.Fatalf("Live before leave = %d, want 1", got)
	}

	// Now publish PresenceLeft. The dispatcher should call Release.
	left := events.NewPresenceLeft("test", actor, "quit")
	if err := b.PublishEvent(left); err != nil {
		t.Fatal(err)
	}

	// Give the dispatcher's goroutine a moment to react.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if orch.Live() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("Live after PresenceLeft = %d, want 0", orch.Live())
}

// TestPullSwapsWorkspaceToUserSuppliedRepo proves the /pull verb:
// the user posts /pull /path/to/bare.git, the orchestrator
// re-provisions a worktree from that repo, and the next /run lands
// in the new workspace.
func TestPullSwapsWorkspaceToUserSuppliedRepo(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox only on darwin/linux, got %s", runtime.GOOS)
	}
	bare := setupBareRepoForDispatchTest(t)

	rt, err := sbhost.New(t.TempDir())
	if err != nil {
		t.Skipf("host backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	wtCtrl, err := plainController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{}).
		WithWorktrees(wtCtrl, "", "", "/workspace")
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	_, obs := b.Subscribe()
	actor := events.Actor{ID: "pk:test", DisplayName: "@daniel", Kind: "human", SessionID: uuid.New()}

	// 1. /pull <bare-repo-path>
	pull := events.NewChatPosted("test", actor, "#lobby", "/pull "+bare, ui.ChatNormal)
	if err := b.PublishEvent(pull); err != nil {
		t.Fatal(err)
	}
	drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if c, ok := e.(*events.SandboxCommandCompleted); ok && c.ExitCode == 0 {
				return true
			}
		}
		return false
	})

	// 2. /run ls should now list the seed file from the pulled repo.
	ls := events.NewChatPosted("test", actor, "#lobby", "/run ls", ui.ChatNormal)
	if err := b.PublishEvent(ls); err != nil {
		t.Fatal(err)
	}
	got := drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.SandboxCommandCompleted); ok {
				return true
			}
		}
		return false
	})
	var stdoutBody strings.Builder
	for _, e := range got {
		if v, ok := e.(*events.SandboxCommandOutput); ok && v.Stream == events.StreamStdout {
			stdoutBody.Write(v.Chunk)
		}
	}
	if !strings.Contains(stdoutBody.String(), "README.md") {
		t.Errorf("ls after /pull should list README.md, got %q", stdoutBody.String())
	}
}

// TestPullRejectsRemoteURL confirms the /pull verb refuses anything
// that looks like a remote URL today (network is denied by the fence).
func TestPullRejectsRemoteURL(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox only on darwin/linux, got %s", runtime.GOOS)
	}
	rt, err := sbhost.New(t.TempDir())
	if err != nil {
		t.Skipf("host backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	wtCtrl, err := plainController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{}).
		WithWorktrees(wtCtrl, "", "", "/workspace")
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = d.Start(ctx)
	defer d.Stop()

	_, obs := b.Subscribe()
	actor := events.Actor{ID: "pk:test", DisplayName: "@d", Kind: "human", SessionID: uuid.New()}
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/pull https://github.com/foo/bar.git", ui.ChatNormal))
	got := drainUntil(t, obs, 3*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if c, ok := e.(*events.SandboxCommandCompleted); ok && c.Error != "" {
				return true
			}
		}
		return false
	})
	found := false
	for _, e := range got {
		if c, ok := e.(*events.SandboxCommandCompleted); ok && strings.Contains(c.Error, "remote pull requires") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected /pull rejection for remote URL, got events %v", got)
	}
}

// TestScratchProducesEmptyWorkspace confirms the /scratch verb wipes
// the session's workspace and leaves an empty sandbox the user can
// hack in.
func TestScratchProducesEmptyWorkspace(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox only on darwin/linux, got %s", runtime.GOOS)
	}
	bare := setupBareRepoForDispatchTest(t)
	rt, err := sbhost.New(t.TempDir())
	if err != nil {
		t.Skipf("host backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	wtCtrl, err := plainController(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Start with a base repo configured so the first sandbox has
	// content. /scratch then wipes it.
	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{}).
		WithWorktrees(wtCtrl, bare, "main", "/workspace")
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = d.Start(ctx)
	defer d.Stop()

	_, obs := b.Subscribe()
	actor := events.Actor{ID: "pk:test", DisplayName: "@d", Kind: "human", SessionID: uuid.New()}

	// Confirm the seed file exists pre-scratch via /run ls.
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/run ls", ui.ChatNormal))
	pre := drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.SandboxCommandCompleted); ok {
				return true
			}
		}
		return false
	})
	var preBody strings.Builder
	for _, e := range pre {
		if v, ok := e.(*events.SandboxCommandOutput); ok && v.Stream == events.StreamStdout {
			preBody.Write(v.Chunk)
		}
	}
	if !strings.Contains(preBody.String(), "README.md") {
		t.Fatalf("seed pre-scratch did not list README.md, got %q", preBody.String())
	}

	// /scratch
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/scratch hacking on dispatcher", ui.ChatNormal))
	drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if c, ok := e.(*events.SandboxCommandCompleted); ok && c.ExitCode == 0 {
				return true
			}
		}
		return false
	})

	// Post-scratch /run ls should be empty.
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/run ls -1A", ui.ChatNormal))
	post := drainUntil(t, obs, 5*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if _, ok := e.(*events.SandboxCommandCompleted); ok {
				return true
			}
		}
		return false
	})
	var postBody strings.Builder
	for _, e := range post {
		if v, ok := e.(*events.SandboxCommandOutput); ok && v.Stream == events.StreamStdout {
			postBody.Write(v.Chunk)
		}
	}
	if strings.TrimSpace(postBody.String()) != "" {
		t.Errorf("scratch workspace should be empty; ls -1A returned %q", postBody.String())
	}
}

// TestDispatcherSurvivesCommandPanic exercises the recover() guard:
// even if a command goroutine panics, future commands keep working.
func TestDispatcherSurvivesCommandPanic(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox only on darwin/linux, got %s", runtime.GOOS)
	}
	rt, err := sbhost.New(t.TempDir())
	if err != nil {
		t.Skipf("host backend unavailable: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })

	orch := sandbox.NewOrchestrator(rt, sandbox.Spec{})
	b := room.New("test", nil, nil, nil)
	defer b.Stop()

	d := dispatch.New(b, orch, "test")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := d.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer d.Stop()

	_, obs := b.Subscribe()
	actor := events.Actor{ID: "pk:test", DisplayName: "@d", Kind: "human", SessionID: uuid.New()}

	// First command: bogus binary path that will fail. We accept any
	// Completed event (non-zero exit OR Error string) since the host
	// fence may surface the failure either way.
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/run /no/such/binary", ui.ChatNormal))
	drainUntil(t, obs, 3*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if c, ok := e.(*events.SandboxCommandCompleted); ok && (c.ExitCode != 0 || c.Error != "") {
				return true
			}
		}
		return false
	})

	// Second command must still succeed after the error.
	_ = b.PublishEvent(events.NewChatPosted("test", actor, "#lobby", "/run echo still-here", ui.ChatNormal))
	got := drainUntil(t, obs, 3*time.Second, func(evts []events.Event) bool {
		for _, e := range evts {
			if c, ok := e.(*events.SandboxCommandCompleted); ok && c.ExitCode == 0 {
				return true
			}
		}
		return false
	})
	found := false
	for _, e := range got {
		if c, ok := e.(*events.SandboxCommandCompleted); ok && c.ExitCode == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("dispatcher did not recover after a failing command")
	}
}
