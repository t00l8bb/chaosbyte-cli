//go:build linux

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"

	"github.com/bchayka/gitstatus/internal/sandbox"
)

// ensureBackend verifies bwrap is on PATH. Bubblewrap is in every
// major distribution's package repos but not always installed by
// default; an early failure here is preferable to a per-spawn one.
func ensureBackend() error {
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("host sandbox: bwrap not found on PATH (apt/dnf/pacman install bubblewrap): %w", err)
	}
	return nil
}

// Exec runs cmd inside the session's fence using bwrap.
//
// The bwrap arguments create a brand-new mount + PID + IPC + UTS +
// user namespace for the process, then bind the host's read-only
// system paths into it. The session tempdir is bind-mounted at
// /workspace; everything outside the namespace is invisible.
// Networking is fully denied (--unshare-all already implies it).
func (s *Sandbox) Exec(ctx context.Context, cmd sandbox.Command) (sandbox.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return nil, sandbox.ErrSandboxDestroyed
	}
	if cmd.Path == "" {
		return nil, errors.New("host sandbox: cmd.Path is required")
	}

	args := bwrapArgs(s.dir, s.spec.Mounts, cmd)
	args = append(args, cmd.Path)
	args = append(args, cmd.Args...)

	c := exec.CommandContext(ctx, "bwrap", args...)
	c.Env = buildEnv(s.spec.Env, cmd.Env)

	p := &Process{cmd: c}

	if cmd.TTY {
		ptmx, err := pty.Start(c)
		if err != nil {
			return nil, fmt.Errorf("host sandbox: pty.Start: %w", err)
		}
		p.pty = ptmx
		p.done = make(chan struct{})
		p.started = true
		go func() {
			err := c.Wait()
			p.mu.Lock()
			p.finished = true
			switch e := err.(type) {
			case nil:
				p.exitCode = 0
			case *exec.ExitError:
				p.exitCode = e.ExitCode()
			default:
				p.exitCode = -1
				p.waitErr = err
			}
			p.mu.Unlock()
			_ = ptmx.Close()
			close(p.done)
		}()
	} else {
		stdin, err := c.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("host sandbox: StdinPipe: %w", err)
		}
		stdout, err := c.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("host sandbox: StdoutPipe: %w", err)
		}
		stderr, err := c.StderrPipe()
		if err != nil {
			return nil, fmt.Errorf("host sandbox: StderrPipe: %w", err)
		}
		p.stdin = stdin
		p.stdout = stdout
		p.stderr = stderr
		if err := p.run(); err != nil {
			return nil, err
		}
	}

	s.procs = append(s.procs, p)
	return p, nil
}

// bwrapArgs returns the argv prefix for bwrap that fences a session.
// The session dir is bound at /session; each Spec.Mount is bound at
// its SandboxPath (read-only or read-write per Mount.ReadOnly). If at
// least one writable mount exists, its SandboxPath becomes the
// default working dir; otherwise we land in /session.
func bwrapArgs(sessionDir string, mounts []sandbox.Mount, cmd sandbox.Command) []string {
	args := []string{
		"--unshare-all",
		"--share-net=no",
		"--die-with-parent",
		"--new-session",
		"--bind", sessionDir, "/session",
		"--ro-bind", "/usr", "/usr",
		"--ro-bind-try", "/bin", "/bin",
		"--ro-bind-try", "/sbin", "/sbin",
		"--ro-bind-try", "/lib", "/lib",
		"--ro-bind-try", "/lib32", "/lib32",
		"--ro-bind-try", "/lib64", "/lib64",
		"--ro-bind-try", "/etc", "/etc",
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	}

	defaultWorkdir := "/session"
	for _, m := range mounts {
		if m.HostPath == "" || m.SandboxPath == "" {
			continue
		}
		if m.ReadOnly {
			args = append(args, "--ro-bind", m.HostPath, m.SandboxPath)
		} else {
			args = append(args, "--bind", m.HostPath, m.SandboxPath)
			if defaultWorkdir == "/session" {
				defaultWorkdir = m.SandboxPath
			}
		}
	}

	workdir := defaultWorkdir
	if cmd.WorkingDir != "" {
		workdir = cmd.WorkingDir
	}
	args = append(args, "--chdir", workdir)
	return args
}

// buildEnv merges spec-level and command-level env, then prepends a
// small set of variables Linux shells expect.
func buildEnv(spec, cmdEnv map[string]string) []string {
	merged := map[string]string{
		"PATH":   os.Getenv("PATH"),
		"HOME":   "/workspace",
		"USER":   os.Getenv("USER"),
		"SHELL":  os.Getenv("SHELL"),
		"LANG":   os.Getenv("LANG"),
		"LC_ALL": os.Getenv("LC_ALL"),
		"TERM":   "xterm-256color",
	}
	for k, v := range spec {
		merged[k] = v
	}
	for k, v := range cmdEnv {
		merged[k] = v
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		if v == "" {
			continue
		}
		out = append(out, k+"="+v)
	}
	return out
}
