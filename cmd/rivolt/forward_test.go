package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/metrics"
)

// Only adopt and fresh_state exist (M3-2b); the other kinds answer 501 and a request that cannot be served answers 400, never an error that would
// look like an upstream failure.
func TestForwardRunnerKinds(t *testing.T) {
	r := &forwardRunner{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, k := range []forward.Kind{forward.KindPlanLegs, forward.KindOTANotes} {
		res, err := r.Run(context.Background(), k, "11111111-2222-3333-4444-555555555555", "g", nil)
		if err != nil || res.Status != 501 || res.Class != "not_implemented" {
			t.Errorf("%s: %+v %v, want 501 not_implemented", k, res, err)
		}
	}
	// fresh_state with nothing wired, or args of the wrong type: 400; a vehicle outside the named group: 404.
	if res, err := r.Run(context.Background(), forward.KindFreshState, "11111111-2222-3333-4444-555555555555", "g", &forward.FreshStateArgs{VehicleID: "v-fake-1"}); err != nil || res.Status != 400 {
		t.Errorf("fresh_state with nothing wired: %+v %v, want 400", res, err)
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
	rt := startForwardOwner(context.Background(), slog.New(slog.NewTextHandler(io.Discard, nil)), m, nil, nil, nil, nil, nil, "rivolt")
	rt.setDraining() // must not panic
	if rt.forwarder != nil {
		t.Error("no requester with the flag off")
	}
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
