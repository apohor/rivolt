package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// Every allowlisted kind has a runner; a kind with none answers 501, and a request that cannot be served answers 400, never an error that would
// look like an upstream failure.
func TestForwardRunnerKinds(t *testing.T) {
	r := &forwardRunner{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, k := range []forward.Kind{"not_a_kind"} {
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

// stateAcct is an account that is also a rivian.Client and counts State calls.
type stateAcct struct {
	rivian.Account
	rivian.Client
	calls int
}

func (a *stateAcct) State(context.Context, string) (*rivian.State, error) {
	a.calls++
	return &rivian.State{VehicleID: "v-fake-1"}, nil
}

func (a *stateAcct) Login(ctx context.Context, c rivian.Credentials) error {
	return a.Account.Login(ctx, c)
}
func (a *stateAcct) Logout()                     { a.Account.Logout() }
func (a *stateAcct) Authenticated() bool         { return true }
func (a *stateAcct) MFAPending() bool            { return false }
func (a *stateAcct) Email() string               { return "" }
func (a *stateAcct) NeedsReauth() (bool, string) { return false, "" }
func (a *stateAcct) Snapshot() rivian.Session    { return rivian.Session{} }
func (a *stateAcct) Restore(rivian.Session)      {}
func (a *stateAcct) AuthReady() <-chan struct{}  { return nil }

type oneAcctRegistry struct{ a *stateAcct }

func (r oneAcctRegistry) For(uuid.UUID) rivian.Account { return r.a }
func (r oneAcctRegistry) Loaded() []uuid.UUID          { return nil }

// A signed fresh_state request cannot make this pod call Rivian for a vehicle outside the group it names: a different group, or a
// vehicle the pod does not know, is 404 not_found with no Rivian call; the right group is one call.
func TestForwardRunnerFreshStateGroupGuard(t *testing.T) {
	store, err := leases.NewStore(new(sql.DB), "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	store.SetVehicleGroups(map[string]string{"v-fake-1": "grp-1"})
	acct := &stateAcct{}
	r := &forwardRunner{store: store, accounts: oneAcctRegistry{acct}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const user = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		name, vehicle, group string
		status, calls        int
	}{
		{"other group", "v-fake-1", "grp-2", 404, 0},
		{"unknown vehicle", "v-fake-9", "grp-1", 404, 0},
		{"right group", "v-fake-1", "grp-1", 200, 1},
	} {
		acct.calls = 0
		res, err := r.Run(context.Background(), forward.KindFreshState, user, tc.group, &forward.FreshStateArgs{VehicleID: tc.vehicle})
		if err != nil || res.Status != tc.status || acct.calls != tc.calls {
			t.Errorf("%s: status %d err %v, %d Rivian calls; want %d and %d", tc.name, res.Status, err, acct.calls, tc.status, tc.calls)
		}
		if tc.status == 404 && res.Class != "not_found" {
			t.Errorf("%s: class %q, want not_found", tc.name, res.Class)
		}
	}
}

// plan_legs has the same guard: a vehicle outside the named group, or unknown, is 404 before any planner is asked; the right group reaches
// the planner (this account has none, so the answer is the plan_error envelope for ErrNoPlanner).
func TestForwardRunnerPlanLegsGroupGuard(t *testing.T) {
	store, err := leases.NewStore(new(sql.DB), "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	store.SetVehicleGroups(map[string]string{"v-fake-1": "grp-1"})
	r := &forwardRunner{store: store, accounts: oneAcctRegistry{&stateAcct{}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	args := func(v string) *forward.PlanLegsArgs { return &forward.PlanLegsArgs{VehicleID: v} }
	const user = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		vehicle, group string
		status         int
		class          string
	}{
		{"v-fake-1", "grp-2", 404, "not_found"},
		{"v-fake-9", "grp-1", 404, "not_found"},
		{"v-fake-1", "grp-1", 502, "plan_error"},
	} {
		res, err := r.Run(context.Background(), forward.KindPlanLegs, user, tc.group, args(tc.vehicle))
		if err != nil || res.Status != tc.status || res.Class != tc.class {
			t.Errorf("%s in %s: %d %q %v, want %d %q", tc.vehicle, tc.group, res.Status, res.Class, err, tc.status, tc.class)
		}
	}
	if res, _ := r.Run(context.Background(), forward.KindPlanLegs, user, "grp-1", "not args"); res.Status != 400 {
		t.Errorf("wrong args type: %d, want 400", res.Status)
	}
}

// The replay test switch is refused anywhere that could reach Rivian, and is off unless asked for.
func TestForwardReplayTestSwitchIsGuarded(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	for name, tc := range map[string]struct {
		getenv func(string) string
		on     bool
		fail   bool
	}{
		"unset":                       {env(), false, false},
		"unset on a live install":     {env("RIVOLT_RIVIAN_LIVE_FORBIDDEN", "", "RIVIAN_CLIENT", "live"), false, false},
		"false":                       {env("RIVOLT_FORWARD_REPLAY_TEST", "false"), false, false},
		"replay and forbidden":        {env("RIVOLT_FORWARD_REPLAY_TEST", "1", "RIVOLT_RIVIAN_LIVE_FORBIDDEN", "true", "RIVIAN_CLIENT", "replay"), true, false},
		"live client":                 {env("RIVOLT_FORWARD_REPLAY_TEST", "1", "RIVOLT_RIVIAN_LIVE_FORBIDDEN", "true", "RIVIAN_CLIENT", "live"), false, true},
		"default client (live)":       {env("RIVOLT_FORWARD_REPLAY_TEST", "1", "RIVOLT_RIVIAN_LIVE_FORBIDDEN", "true"), false, true},
		"replay but live not barred":  {env("RIVOLT_FORWARD_REPLAY_TEST", "1", "RIVIAN_CLIENT", "replay"), false, true},
		"mock is not replay":          {env("RIVOLT_FORWARD_REPLAY_TEST", "1", "RIVOLT_RIVIAN_LIVE_FORBIDDEN", "true", "RIVIAN_CLIENT", "mock"), false, true},
		"nothing else set, switch on": {env("RIVOLT_FORWARD_REPLAY_TEST", "true"), false, true},
	} {
		on, err := forwardReplayTest(tc.getenv)
		if on != tc.on || (err != nil) != tc.fail {
			t.Errorf("%s: on=%v err=%v, want on=%v fail=%v", name, on, err, tc.on, tc.fail)
		}
	}
}

// ota_notes has the same group guard: a vehicle outside the named group, or unknown, is 404 before Rivian is asked.
func TestForwardRunnerOTANotesGroupGuard(t *testing.T) {
	store, err := leases.NewStore(new(sql.DB), "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	store.SetVehicleGroups(map[string]string{"v-fake-1": "grp-1"})
	r := &forwardRunner{store: store, accounts: oneAcctRegistry{&stateAcct{}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	const user = "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		vehicle, group string
		status         int
	}{{"v-fake-1", "grp-2", 404}, {"v-fake-9", "grp-1", 404}, {"v-fake-1", "grp-1", 200}} {
		res, err := r.Run(context.Background(), forward.KindOTANotes, user, tc.group, &forward.OTANotesArgs{VehicleID: tc.vehicle})
		if err != nil || res.Status != tc.status {
			t.Errorf("%s in %s: %d %v, want %d", tc.vehicle, tc.group, res.Status, err, tc.status)
		}
	}
}

type nilAcctRegistry struct{}

func (nilAcctRegistry) For(uuid.UUID) rivian.Account { return nil }
func (nilAcctRegistry) Loaded() []uuid.UUID          { return nil }

// With RIVOLT_NO_STANDING_TOKENS a pod that does not hold the user's group has no client: an adopt nudge that reaches it is 503 not_owner,
// answered before the stored session is read.
func TestForwardRunnerAdoptOnAnUnheldPod(t *testing.T) {
	r := &forwardRunner{secrets: &secrets.Store{}, accounts: nilAcctRegistry{}, monitors: &rivian.MonitorRegistry{}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	res, err := r.adopt(context.Background(), uuid.NewString())
	if err != nil || res.Status != 503 || res.Class != "not_owner" {
		t.Fatalf("adopt = %+v, %v; want 503 not_owner", res, err)
	}
}
