package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/metrics"
)

// Only adopt exists in M3-1; the other kinds answer 501 and a request that cannot be served answers 400, never an error that would
// look like an upstream failure.
func TestForwardRunnerKinds(t *testing.T) {
	r := &forwardRunner{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, k := range []forward.Kind{forward.KindPlanLegs, forward.KindFreshState, forward.KindAdminRefresh, forward.KindOTANotes} {
		res, err := r.Run(context.Background(), k, "11111111-2222-3333-4444-555555555555", "g", nil)
		if err != nil || res.Status != 501 || res.Class != "not_implemented" {
			t.Errorf("%s: %+v %v, want 501 not_implemented", k, res, err)
		}
	}
	// adopt with nothing wired, and with a user that is not a uuid: 400.
	for _, user := range []string{"11111111-2222-3333-4444-555555555555", "not-a-uuid"} {
		res, err := r.Run(context.Background(), forward.KindAdopt, user, "g", nil)
		if err != nil || res.Status != 400 {
			t.Errorf("adopt for %q: %+v %v, want 400", user, res, err)
		}
	}
}

// With RIVOLT_FORWARD unset (the default) nothing runs: the metrics exist at 0 and the draining hook is a no-op.
func TestStartForwardOwnerIsOffByDefault(t *testing.T) {
	t.Setenv("RIVOLT_FORWARD", "")
	m := metrics.New()
	drain := startForwardOwner(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), m, nil, nil, nil, nil, nil, "rivolt")
	drain() // must not panic
	mfs, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mf := range mfs {
		if mf.GetName() == "rivolt_rivian_forward_total" {
			found = true
			if got := len(mf.GetMetric()); got != (len(forward.Kinds)+1)*len(forward.Outcomes) {
				t.Errorf("forward_total series = %d, want every kind and outcome at 0", got)
			}
		}
	}
	if !found {
		t.Error("rivolt_rivian_forward_total must be registered even with the flag off")
	}
}
