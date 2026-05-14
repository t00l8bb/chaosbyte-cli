// Package mock is the in-process Runtime used by unit tests. It does
// not isolate; commands run in the same process as the caller via a
// fake exec model. The mock is permanent: it lives in the codebase as
// the unit-test substrate so we never need to spin up a real backend
// to exercise the orchestrator.
//
// Production code paths never see mock. The host backend (sandbox-exec
// on Darwin, bwrap on Linux) is what cmd/vibespace-server constructs.
package mock

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bchayka/gitstatus/internal/sandbox"
)

// Runtime is the in-process Runtime implementation.
type Runtime struct {
	mu       sync.Mutex
	closed   bool
	spawned  []*Sandbox
	spawnsN  int64
	destroysN int64
}

// New returns a fresh mock Runtime.
func New() *Runtime {
	return &Runtime{}
}

// Spawn returns a new in-process Sandbox.
func (r *Runtime) Spawn(_ context.Context, spec sandbox.Spec) (sandbox.Sandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, sandbox.ErrRuntimeClosed
	}
	s := &Sandbox{
		id:        sandbox.NewID(),
		spec:      spec,
		createdAt: time.Now(),
		runtime:   r,
	}
	r.spawned = append(r.spawned, s)
	atomic.AddInt64(&r.spawnsN, 1)
	return s, nil
}

// Close marks the runtime closed and destroys every live sandbox.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	for _, s := range r.spawned {
		_ = s.destroyLocked()
	}
	return nil
}

// Kind returns "mock".
func (r *Runtime) Kind() string { return "mock" }

// Stats returns the cumulative spawn and destroy counts. Useful in
// tests to assert lifecycle hygiene.
func (r *Runtime) Stats() (spawns, destroys int64) {
	return atomic.LoadInt64(&r.spawnsN), atomic.LoadInt64(&r.destroysN)
}

// Sandbox is the mock implementation of sandbox.Sandbox.
type Sandbox struct {
	id        sandbox.ID
	spec      sandbox.Spec
	createdAt time.Time
	runtime   *Runtime

	mu        sync.Mutex
	destroyed bool
	processes []*Process
}

// ID returns the unique identifier assigned at Spawn.
func (s *Sandbox) ID() sandbox.ID { return s.id }

// CreatedAt returns the spawn time.
func (s *Sandbox) CreatedAt() time.Time { return s.createdAt }

// Exec creates a new Process inside the mock sandbox. The mock does
// not run real commands; tests script the output via SetOutput before
// invoking Exec, or read what they wrote to Stdin.
func (s *Sandbox) Exec(_ context.Context, cmd sandbox.Command) (sandbox.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.destroyed {
		return nil, sandbox.ErrSandboxDestroyed
	}
	p := newProcess(cmd)
	s.processes = append(s.processes, p)
	return p, nil
}

// Destroy tears the sandbox down. Idempotent.
func (s *Sandbox) Destroy(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.destroyLocked()
}

func (s *Sandbox) destroyLocked() error {
	if s.destroyed {
		return nil
	}
	s.destroyed = true
	for _, p := range s.processes {
		p.terminate(137) // SIGKILL exit convention
	}
	atomic.AddInt64(&s.runtime.destroysN, 1)
	return nil
}

// Process is the mock sandbox.Process. Stdin/Stdout/Stderr are real
// in-process pipes. PTY returns a single pipe pair when TTY is true.
type Process struct {
	cmd sandbox.Command

	stdin  *pipeWriter
	stdout *pipeReader
	stderr *pipeReader
	pty    *ptyPair

	doneCh   chan struct{}
	exitCode int
	pid      int

	mu        sync.Mutex
	terminated bool

	// scriptedOutput, if set via SetOutput, is written to stdout when
	// the process is started.
	scriptedOutput []byte
	scriptedExit   int
}

func newProcess(cmd sandbox.Command) *Process {
	p := &Process{
		cmd:    cmd,
		doneCh: make(chan struct{}),
		pid:    int(time.Now().UnixNano() & 0x7fff),
	}
	p.stdin = newPipeWriter()
	p.stdout = newPipeReader()
	p.stderr = newPipeReader()
	if cmd.TTY {
		p.pty = newPtyPair()
	}
	return p
}

// SetOutput scripts stdout output and the exit code for the next
// implicit "run" the test triggers by calling Wait.
func (p *Process) SetOutput(stdout []byte, exit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scriptedOutput = stdout
	p.scriptedExit = exit
}

// Stdin returns the writable end of the process's standard input.
func (p *Process) Stdin() io.WriteCloser { return p.stdin }

// Stdout returns the readable end of the process's standard output.
func (p *Process) Stdout() io.ReadCloser { return p.stdout }

// Stderr returns the readable end of the process's standard error.
func (p *Process) Stderr() io.ReadCloser { return p.stderr }

// PTY returns the PTY master if Command.TTY was true. The mock's PTY
// is a single pipe pair: writes flow back to readers.
func (p *Process) PTY() io.ReadWriteCloser {
	if p.pty == nil {
		return nil
	}
	return p.pty
}

// Wait blocks until terminate is called and returns the exit code.
func (p *Process) Wait(ctx context.Context) (int, error) {
	// Flush scripted output before signalling done.
	p.mu.Lock()
	if len(p.scriptedOutput) > 0 {
		_, _ = p.stdout.Write(p.scriptedOutput)
		p.exitCode = p.scriptedExit
		p.scriptedOutput = nil
		go p.terminate(p.scriptedExit)
	}
	p.mu.Unlock()

	select {
	case <-ctx.Done():
		p.terminate(130)
		return 130, ctx.Err()
	case <-p.doneCh:
		return p.exitCode, nil
	}
}

// Signal accepts every Signal in the mock. Kill terminates with 137.
func (p *Process) Signal(sig sandbox.Signal) error {
	switch sig {
	case sandbox.SignalKill:
		p.terminate(137)
	case sandbox.SignalTerm:
		p.terminate(143)
	case sandbox.SignalInt:
		p.terminate(130)
	case sandbox.SignalHUP:
		p.terminate(129)
	default:
		return sandbox.ErrUnsupportedSignal
	}
	return nil
}

// PID returns the synthetic process id.
func (p *Process) PID() int { return p.pid }

func (p *Process) terminate(code int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.terminated {
		return
	}
	p.terminated = true
	p.exitCode = code
	p.stdout.Close()
	p.stderr.Close()
	if p.pty != nil {
		p.pty.Close()
	}
	close(p.doneCh)
}

// ---- pipe helpers ----

// pipeWriter is a minimal WriteCloser backed by an internal buffer.
type pipeWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
}

func newPipeWriter() *pipeWriter { return &pipeWriter{} }

func (w *pipeWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	return w.buf.Write(p)
}

func (w *pipeWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	return nil
}

// pipeReader is a ReadCloser backed by an internal buffer; tests write
// to it via Write and Close it via Close.
type pipeReader struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	closed bool
	wake   chan struct{}
}

func newPipeReader() *pipeReader { return &pipeReader{wake: make(chan struct{}, 1)} }

func (r *pipeReader) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := r.buf.Write(p)
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return n, err
}

func (r *pipeReader) Read(p []byte) (int, error) {
	for {
		r.mu.Lock()
		if r.buf.Len() > 0 {
			n, _ := r.buf.Read(p)
			r.mu.Unlock()
			return n, nil
		}
		if r.closed {
			r.mu.Unlock()
			return 0, io.EOF
		}
		r.mu.Unlock()
		<-r.wake
	}
}

func (r *pipeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	select {
	case r.wake <- struct{}{}:
	default:
	}
	return nil
}

// ptyPair is a mock pseudo-terminal: a single bidirectional pipe pair.
type ptyPair struct {
	r *pipeReader
	w *pipeWriter
}

func newPtyPair() *ptyPair {
	return &ptyPair{r: newPipeReader(), w: newPipeWriter()}
}

func (p *ptyPair) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *ptyPair) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *ptyPair) Close() error {
	_ = p.r.Close()
	_ = p.w.Close()
	return nil
}

// Assert *Runtime satisfies sandbox.Runtime at compile time.
var _ sandbox.Runtime = (*Runtime)(nil)
var _ sandbox.Sandbox = (*Sandbox)(nil)
var _ sandbox.Process = (*Process)(nil)
var _ = errors.New
