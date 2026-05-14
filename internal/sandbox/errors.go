package sandbox

import "errors"

// ErrRuntimeClosed is returned by Spawn after the Runtime has been
// closed.
var ErrRuntimeClosed = errors.New("sandbox: runtime is closed")

// ErrSandboxDestroyed is returned by Exec on a sandbox whose Destroy
// has been called.
var ErrSandboxDestroyed = errors.New("sandbox: sandbox is destroyed")

// ErrProcessNotTTY is returned by Process.PTY when the command was
// not requested with TTY=true.
var ErrProcessNotTTY = errors.New("sandbox: process was not requested with TTY")

// ErrUnsupportedSignal is returned by Process.Signal for signals the
// backend cannot deliver.
var ErrUnsupportedSignal = errors.New("sandbox: unsupported signal")
