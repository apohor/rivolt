//go:build integration

package main

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// Single-owner M4-3 through the sink main.go installs on every client (newReauthSink over the real lease store and Postgres): with
// RIVOLT_SINGLE_OWNER on, a rejection seen by the pod that owns the user's group raises needs_reauth (carries the token), one seen by another pod
// raises nothing and is counted refused{not_owner} (its carries is "none"), and a user with no vehicle, who has no group and so no owner, is raised
// as unowned. carries="none" is exactly the refused count: nothing reaches the database without a token, an unowned mark or a refusal.
func TestReauthSinkOwnerOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db.SetReauthRequiresOwner(true)
	t.Cleanup(func() { db.SetReauthRequiresOwner(false) })

	store := func(pod string) *leases.Store {
		s, err := leases.NewStore(pool, pod)
		if err != nil {
			t.Fatal(err)
		}
		s.SetGroupLead(true)
		return s
	}
	podA, podB := store("pod-a"), store("pod-b")

	user := func(name string, vehicles bool) (uuid.UUID, time.Time) {
		uid, err := db.EnsureUser(ctx, pool, name+"-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatal(err)
		}
		if vehicles {
			if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2)`, uid, "veh-"+uuid.NewString()[:8]); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'k')`, uid); err != nil {
			t.Fatal(err)
		}
		var ver time.Time
		if err := pool.QueryRowContext(ctx, `SELECT updated_at FROM user_secrets WHERE user_id = $1 AND name = 'rivian.session'`, uid).Scan(&ver); err != nil {
			t.Fatal(err)
		}
		return uid, ver
	}
	owned, ownedVer := user("sink-owned", true)
	other, otherVer := user("sink-other", true)
	none, noneVer := user("sink-novehicle", false)

	// pod-a takes every group that exists now; pod-b takes none of them (LeadGroups on b finds them held).
	if _, err := podA.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := podB.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}

	m := metrics.New()
	var raised atomic.Int32
	sink := func(f userFencer, uid uuid.UUID) func(context.Context, string) {
		return newReauthSink(reauthSinkDeps{
			pool: pool, fencer: f, metrics: m, logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			synced:   func() bool { return true },
			onRaised: func(string) { raised.Add(1) },
		}, uid)
	}
	flagged := func(uid uuid.UUID) bool {
		needs, _, err := db.GetNeedsReauth(ctx, pool, uid)
		if err != nil {
			t.Fatal(err)
		}
		return needs
	}
	carries := func(l string) float64 { return testutil.ToFloat64(m.ReauthRaiseAttempts.WithLabelValues(l)) }
	refused := func(l string) float64 { return testutil.ToFloat64(m.ReauthRaiseRefused.WithLabelValues(l)) }
	counts := func() [5]float64 {
		return [5]float64{carries("token"), carries("unowned"), carries("none"), refused("not_owner"), refused("fenced")}
	}
	// token, unowned, none, refused not_owner, refused fenced
	check := func(name string, want [5]float64) {
		t.Helper()
		if got := counts(); got != want {
			t.Errorf("%s: counters (token, unowned, none, not_owner, fenced) = %v, want %v", name, got, want)
		}
	}
	check("start", [5]float64{})

	// 1. The pod that owns the group raises.
	sink(podA, owned)(rivian.WithSessionVersion(ctx, ownedVer), "rejected")
	if !flagged(owned) || raised.Load() != 1 {
		t.Errorf("owner pod: flagged=%v emails=%d, want the raise and one new transition", flagged(owned), raised.Load())
	}
	check("owner raise", [5]float64{1, 0, 0, 0, 0})

	// 2. Another pod's rejection of the same kind of user raises nothing and is counted refused{not_owner}.
	sink(podB, other)(rivian.WithSessionVersion(ctx, otherVer), "rejected")
	if flagged(other) || raised.Load() != 1 {
		t.Errorf("non-owner pod: flagged=%v emails=%d, want nothing written", flagged(other), raised.Load())
	}
	check("non-owner raise", [5]float64{1, 0, 1, 1, 0})

	// 3. A user with no vehicle has no group and no owner: raised as unowned, by whichever pod sees the rejection.
	sink(podB, none)(rivian.WithSessionVersion(ctx, noneVer), "rejected")
	if !flagged(none) || raised.Load() != 2 {
		t.Errorf("vehicle-less user: flagged=%v emails=%d, want the raise as unowned", flagged(none), raised.Load())
	}
	check("vehicle-less raise", [5]float64{1, 1, 1, 1, 0})

	// A rejection of a stale session version raises nothing even for the owner, and is not a refusal.
	stale, staleVer := user("sink-stale", true)
	if _, err := podA.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	sink(podA, stale)(rivian.WithSessionVersion(ctx, staleVer.Add(-time.Hour)), "rejected")
	if flagged(stale) {
		t.Error("a stale session version raised needs_reauth")
	}
}
