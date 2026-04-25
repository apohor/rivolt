package rivian

import (
	"context"
	"errors"
	"testing"
)

// TestUpstreamGateShortCircuits proves the kill-switch contract:
// when the gate returns an error, no HTTP traffic is emitted and
// callers receive that error verbatim. The stub gateway records
// every inbound call, so "zero captured" is our proof that we
// didn't leak a request.
func TestUpstreamGateShortCircuits(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL).WithUpstreamGate(func(context.Context) error {
		return ErrUpstreamPaused
	})

	if err := c.ensureCSRF(context.Background()); !errors.Is(err, ErrUpstreamPaused) {
		t.Fatalf("ensureCSRF err = %v, want ErrUpstreamPaused", err)
	}

	// The whole point of the kill switch: upstream sees nothing.
	// If this count is nonzero we've shipped a hot-path bypass
	// that defeats the incident response story the flag exists
	// for.
	if n := len(g.capturedReqs); n != 0 {
		t.Fatalf("gate open: want 0 captured requests, got %d", n)
	}
}

// TestUpstreamGateOpenAllowsTraffic keeps us honest that "no gate"
// and "gate returns nil" are both the pass-through path. Without
// this, a future refactor that e.g. panics on nil-check before the
// call could pass the short-circuit test but break every real
// deployment.
func TestUpstreamGateOpenAllowsTraffic(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL).WithUpstreamGate(func(context.Context) error {
		return nil
	})

	if err := c.ensureCSRF(context.Background()); err != nil {
		t.Fatalf("ensureCSRF: %v", err)
	}
	if n := len(g.capturedReqs); n != 1 {
		t.Fatalf("gate open: want 1 captured request, got %d", n)
	}
}
