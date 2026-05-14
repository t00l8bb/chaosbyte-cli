package agent

import (
	"context"
	"fmt"
	"strings"
)

// Stub is a no-op Agent used in tests and when no real backend is
// configured. It echoes the prompt back as text and never invokes
// tools. Production code paths never construct a Stub; the daemon
// builds either a Claude agent (env-gated) or refuses to spawn an
// agent when no backend is available.
type Stub struct {
	prefix string
}

// NewStub returns a Stub that prefixes its replies with "stub:" so
// it is obvious in test output. An optional custom prefix can be
// provided for variants.
func NewStub(prefix string) *Stub {
	if prefix == "" {
		prefix = "stub"
	}
	return &Stub{prefix: prefix}
}

func (s *Stub) Kind() string { return "stub" }

func (s *Stub) Step(_ context.Context, prompt string) (Response, error) {
	body := strings.TrimSpace(prompt)
	if body == "" {
		body = "(empty prompt)"
	}
	return Response{
		Text: fmt.Sprintf("%s: heard %q", s.prefix, body),
	}, nil
}

var _ Agent = (*Stub)(nil)
