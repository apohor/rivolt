//go:build integration

package api

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// A Rivian sign-in that finds no vehicle asks the owner of the user's group to look them up now; one that finds vehicles does not. The hook
// reaches the handler as the route mounts it (withSignInNudge), and the nudge goes out on the password leg.
func TestSignInWithNoVehicleNudgesDiscovery(t *testing.T) {
	ctx, pool := openFunnelDB(t)
	store := secrets.New(pool, crypto.NoopSealer{})
	for _, tc := range []struct {
		name     string
		vehicles []rivian.Vehicle
		want     int32
	}{
		{"no vehicle", nil, 1},
		{"has a vehicle", []rivian.Vehicle{{ID: "veh-nudge", Name: "Blue", Model: "R1S"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, err := db.EnsureUser(ctx, pool, "nudge-"+uuid.NewString()[:8])
			if err != nil {
				t.Fatal(err)
			}
			mock := rivian.NewMock()
			mock.SetVehicles(tc.vehicles)
			reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient { return mock })
			var got atomic.Int32
			var nudged atomic.Value
			h := withSignInNudge(func(_ context.Context, u uuid.UUID) { got.Add(1); nudged.Store(u) })(
				handleRivianLogin(reg, store, nil, nil, pool, metrics.New(), nil))
			code, _ := postJSON(t, ctx, func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }, uid, rivianLoginReq{Email: "d@example.invalid", Password: "pw"})
			if code != http.StatusOK {
				t.Fatalf("login = %d", code)
			}
			if got.Load() != tc.want {
				t.Fatalf("nudges = %d, want %d", got.Load(), tc.want)
			}
			if tc.want == 1 && nudged.Load() != uid {
				t.Errorf("nudged %v, want the signed-in user", nudged.Load())
			}
		})
	}
}

// The receipts hook goes out once per completed sign-in whatever the vehicle count (the discovery nudge does not: it asks only for a user with
// no vehicle), and the unheld-refresh path, which clients poll, never sends it.
func TestSignInReceiptsHookFiresForAnyVehicleCount(t *testing.T) {
	ctx, pool := openFunnelDB(t)
	store := secrets.New(pool, crypto.NoopSealer{})
	for _, vehicles := range [][]rivian.Vehicle{nil, {{ID: "veh-rcpt", Name: "Blue", Model: "R1S"}}} {
		uid, err := db.EnsureUser(ctx, pool, "rcpt-"+uuid.NewString()[:8])
		if err != nil {
			t.Fatal(err)
		}
		mock := rivian.NewMock()
		mock.SetVehicles(vehicles)
		reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient { return mock })
		var hook, nudge atomic.Int32
		h := withSignInNudge(func(context.Context, uuid.UUID) { nudge.Add(1) })(
			withReceiptsSignIn(func(context.Context, uuid.UUID) { hook.Add(1) })(handleRivianLogin(reg, store, nil, nil, pool, metrics.New(), nil)))
		if code, _ := postJSON(t, ctx, func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }, uid, rivianLoginReq{Email: "d@example.invalid", Password: "pw"}); code != http.StatusOK {
			t.Fatalf("login = %d", code)
		}
		if hook.Load() != 1 {
			t.Errorf("%d vehicles: receipts hook fired %d times, want 1", len(vehicles), hook.Load())
		}
	}
}
