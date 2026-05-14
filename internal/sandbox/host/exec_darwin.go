//go:build darwin

package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/creack/pty"

	"github.com/bchayka/gitstatus/internal/sandbox"
)

// ensureBackend verifies sandbox-exec exists on PATH. It ships with
// every macOS install since 10.5, so this is essentially a smoke
// check.
func ensureBackend() error {
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return fmt.Errorf("host sandbox: sandbox-exec not found: %w", err)
	}
	return nil
}

// Exec runs cmd inside the session's fence using sandbox-exec.
//
// The generated SBPL profile denies by default and selectively
// re-allows the operations a normal shell needs: forking, exec,
// signal-to-self, reading the host filesystem, writing into the
// session tempdir, and a small set of tmp directories Go and the
// shell tools rely on. Network is fully denied.
func (s *Sandbox) Exec(ctx context.Context, cmd sandbox.Command) (sandbox.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return nil, sandbox.ErrSandboxDestroyed
	}
	if cmd.Path == "" {
		return nil, errors.New("host sandbox: cmd.Path is required")
	}

	profile := sbplProfile(s.dir, s.spec.Mounts)
	args := []string{"-p", profile, cmd.Path}
	args = append(args, cmd.Args...)

	c := exec.CommandContext(ctx, "sandbox-exec", args...)
	c.Dir = resolveWorkDir(s.dir, cmd.WorkingDir, s.spec.Mounts)
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
		// We started via pty.Start; mirror process.run's bookkeeping by
		// hand so Wait/Signal still work.
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

// sbplProfile returns the sandbox-exec profile (SBPL) that fences a
// process to the session directory and any extra Spec.Mounts.
//
// The profile is written as a Scheme-style s-expression. The notable
// rules:
//
//   - deny default		: nothing is allowed unless explicitly listed
//   - allow process-*		: shell needs to fork/exec other binaries
//   - allow signal (target self): processes can signal themselves
//   - allow file-read*		: read access to the whole host
//     filesystem so /usr/bin/clang, /etc/resolv.conf, dylibs, etc.
//     all resolve normally
//   - allow file-write*	: write access limited to the session tempdir,
//     each writable mount, plus a small allowlist of paths Go, the
//     shell, and friends need for their own scratch state
//   - allow mach-lookup		: macOS services lookup
//   - deny network*		: hard network-deny
func sbplProfile(sessionDir string, mounts []sandbox.Mount) string {
	q := func(s string) string { return strings.ReplaceAll(s, `"`, `\"`) }

	var writableSubpaths []string
	writableSubpaths = append(writableSubpaths, `  (subpath "`+q(sessionDir)+`")`)
	for _, m := range mounts {
		if m.ReadOnly || m.HostPath == "" {
			continue
		}
		writableSubpaths = append(writableSubpaths, `  (subpath "`+q(m.HostPath)+`")`)
	}
	writableSubpaths = append(writableSubpaths,
		`  (subpath "/private/tmp")`,
		`  (subpath "/private/var/tmp")`,
		`  (subpath "/private/var/folders")`,
		`  (regex #"^/dev/(null|tty|zero|urandom|random|stdin|stdout|stderr|fd/.*|pty.*)$")`,
	)

	return `
(version 1)
(deny default)

(allow process-fork)
(allow process-exec*)
(allow signal (target self))
(allow sysctl-read)
(allow ipc-posix-shm*)
(allow mach-lookup)
(allow system-socket)
(allow file-ioctl)

(allow file-read*)

(allow file-write*
` + strings.Join(writableSubpaths, "\n") + `)

(deny network*)
`
}

// resolveWorkDir returns the absolute working directory for the
// process. Precedence: explicit cmd.WorkingDir > first writable
// Mount.HostPath > session dir.
func resolveWorkDir(sessionDir, requested string, mounts []sandbox.Mount) string {
	if requested != "" {
		return requested
	}
	for _, m := range mounts {
		if !m.ReadOnly && m.HostPath != "" {
			return m.HostPath
		}
	}
	return sessionDir
}

// buildEnv merges the Sandbox-level env with the Command-level env.
// The Command env overrides. PATH is inherited from the parent process
// so the command can find binaries.
func buildEnv(spec, cmdEnv map[string]string) []string {
	merged := map[string]string{
		"PATH":   os.Getenv("PATH"),
		"HOME":   os.Getenv("HOME"),
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
