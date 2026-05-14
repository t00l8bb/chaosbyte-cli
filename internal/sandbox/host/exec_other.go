//go:build !darwin && !linux

package host

import (
	"context"
	"errors"

	"github.com/bchayka/gitstatus/internal/sandbox"
)

// ensureBackend always fails on platforms without a supported
// process-isolation primitive. The Runtime constructor will surface
// this before any sandbox is spawned.
func ensureBackend() error {
	return errors.New("host sandbox: backend not implemented for this OS (need darwin or linux)")
}

// Exec is unreachable on unsupported platforms but defined for the
// interface assertion.
func (s *Sandbox) Exec(_ context.Context, _ sandbox.Command) (sandbox.Process, error) {
	return nil, errors.New("host sandbox: backend not implemented for this OS")
}
