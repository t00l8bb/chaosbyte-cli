package host_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bchayka/gitstatus/internal/sandbox"
	"github.com/bchayka/gitstatus/internal/sandbox/host"
)

// requireBackend skips the test on platforms that do not have a
// real backend (e.g. running CI on Windows).
func requireBackend(t *testing.T) *host.Runtime {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("host sandbox backend only implemented for darwin/linux, got %s", runtime.GOOS)
	}
	rt, err := host.New(t.TempDir())
	if err != nil {
		t.Skipf("backend tooling unavailable on this host: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func TestSpawnReturnsSandbox(t *testing.T) {
	rt := requireBackend(t)
	s, err := rt.Spawn(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if s.ID().String() == "" {
		t.Error("Sandbox should have a non-empty ID")
	}
	if got := s.(*host.Sandbox).Dir(); got == "" {
		t.Error("Sandbox should expose its session dir")
	}
}

func TestExecCapturesStdout(t *testing.T) {
	rt := requireBackend(t)
	s, err := rt.Spawn(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := s.Exec(context.Background(), sandbox.Command{
		Path: "/bin/echo",
		Args: []string{"hello sandbox"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	out, _ := io.ReadAll(proc.Stdout())
	exit, err := proc.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if got := strings.TrimSpace(string(out)); got != "hello sandbox" {
		t.Errorf("stdout = %q, want %q", got, "hello sandbox")
	}
}

func TestExecCanWriteInsideSessionDir(t *testing.T) {
	rt := requireBackend(t)
	s, err := rt.Spawn(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	hs := s.(*host.Sandbox)
	// On Linux the session dir is bind-mounted at /workspace; on Darwin
	// we keep the absolute host path. Use a shell that picks the right
	// target via cwd so the test stays portable.
	proc, err := s.Exec(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo inside > marker.txt"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exit, err := proc.Wait(context.Background()); err != nil || exit != 0 {
		t.Fatalf("Wait: exit=%d err=%v", exit, err)
	}
	// The file must exist on the host inside the session dir.
	target := filepath.Join(hs.Dir(), "marker.txt")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("expected file at %s: %v", target, err)
	}
	if strings.TrimSpace(string(body)) != "inside" {
		t.Errorf("marker contents = %q, want %q", string(body), "inside")
	}
}

func TestExecCannotEscapeSessionDir(t *testing.T) {
	rt := requireBackend(t)
	s, err := rt.Spawn(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	// /tmp and /var/folders are intentionally writable (Go scratch
	// space and POSIX temp). Target $HOME instead, which the profile
	// does not whitelist.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("UserHomeDir unavailable: %v", err)
	}
	marker := filepath.Join(home, ".host-sandbox-fence-test-"+s.ID().String())
	_ = os.Remove(marker)
	proc, err := s.Exec(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "echo leaked > " + marker + " 2>/dev/null; true"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	_, _ = proc.Wait(context.Background())
	if _, err := os.Stat(marker); err == nil {
		_ = os.Remove(marker)
		t.Errorf("sandbox allowed write to %s; fence is broken", marker)
	}
}

func TestDestroyRemovesSessionDir(t *testing.T) {
	rt := requireBackend(t)
	s, err := rt.Spawn(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	dir := s.(*host.Sandbox).Dir()
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("session dir should exist before Destroy: %v", err)
	}
	if err := s.Destroy(context.Background()); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("session dir should be gone after Destroy, got err=%v", err)
	}
}

func TestCloseTearsDownAll(t *testing.T) {
	rt := requireBackend(t)
	dirs := []string{}
	for i := 0; i < 3; i++ {
		s, err := rt.Spawn(context.Background(), sandbox.Spec{})
		if err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, s.(*host.Sandbox).Dir())
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("dir %s should be gone after Close", d)
		}
	}
	spawns, destroys := rt.Stats()
	if spawns != 3 || destroys != 3 {
		t.Errorf("spawns=%d destroys=%d, want 3 and 3", spawns, destroys)
	}
}

func TestWaitContextCancelKillsProcess(t *testing.T) {
	rt := requireBackend(t)
	s, err := rt.Spawn(context.Background(), sandbox.Spec{})
	if err != nil {
		t.Fatal(err)
	}
	proc, err := s.Exec(context.Background(), sandbox.Command{
		Path: "/bin/sh",
		Args: []string{"-c", "sleep 30"},
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err = proc.Wait(ctx)
	if err == nil {
		t.Error("Wait should return context error on cancel")
	}
}
