package host

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/bchayka/gitstatus/internal/sandbox"
)

// Process wraps an exec.Cmd that was spawned under the host sandbox
// wrapper (sandbox-exec on Darwin, bwrap on Linux). The wrapper is
// transparent to callers: Stdin/Stdout/Stderr/PTY behave as if the
// caller had run the command directly.
type Process struct {
	cmd *exec.Cmd

	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	pty    io.ReadWriteCloser

	mu       sync.Mutex
	started  bool
	finished bool
	exitCode int
	waitErr  error
	done     chan struct{}
}

// Stdin returns the writable end of the process's standard input. For
// PTY processes this is nil; write to the PTY ReadWriter instead.
func (p *Process) Stdin() io.WriteCloser { return p.stdin }

// Stdout returns the readable end of standard output. nil for PTY.
func (p *Process) Stdout() io.ReadCloser { return p.stdout }

// Stderr returns the readable end of standard error. nil for PTY
// (PTY merges both streams).
func (p *Process) Stderr() io.ReadCloser { return p.stderr }

// PTY returns the pseudo-terminal master if the command was started
// with Command.TTY = true, nil otherwise.
func (p *Process) PTY() io.ReadWriteCloser { return p.pty }

// PID returns the operating-system process id, or 0 if not started.
func (p *Process) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Wait blocks until the process exits or the context is cancelled.
// Cancelling the context sends SIGKILL.
func (p *Process) Wait(ctx context.Context) (int, error) {
	select {
	case <-p.done:
		return p.exitCode, p.waitErr
	case <-ctx.Done():
		_ = p.Signal(sandbox.SignalKill)
		<-p.done
		return p.exitCode, ctx.Err()
	}
}

// Signal sends sig to the process. Returns ErrUnsupportedSignal for
// signals this backend does not recognize.
func (p *Process) Signal(sig sandbox.Signal) error {
	if p.cmd == nil || p.cmd.Process == nil {
		return errors.New("host sandbox: process not started")
	}
	native, ok := translateSignal(sig)
	if !ok {
		return sandbox.ErrUnsupportedSignal
	}
	return p.cmd.Process.Signal(native)
}

func translateSignal(sig sandbox.Signal) (os.Signal, bool) {
	switch sig {
	case sandbox.SignalTerm:
		return syscall.SIGTERM, true
	case sandbox.SignalKill:
		return syscall.SIGKILL, true
	case sandbox.SignalInt:
		return syscall.SIGINT, true
	case sandbox.SignalHUP:
		return syscall.SIGHUP, true
	}
	return nil, false
}

// run starts cmd and arranges p.done to close when the command exits.
// Callers wire stdin/stdout/stderr or pty before calling run.
func (p *Process) run() error {
	p.mu.Lock()
	if p.started {
		p.mu.Unlock()
		return errors.New("host sandbox: process already started")
	}
	p.started = true
	p.done = make(chan struct{})
	p.mu.Unlock()

	if err := p.cmd.Start(); err != nil {
		close(p.done)
		return fmt.Errorf("host sandbox: start: %w", err)
	}

	go func() {
		err := p.cmd.Wait()
		p.mu.Lock()
		p.finished = true
		p.waitErr = nil
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
		// Close stdio pipes so readers unblock.
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		close(p.done)
	}()
	return nil
}

// Assert at compile time.
var _ sandbox.Process = (*Process)(nil)
