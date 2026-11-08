//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// Golden bodies for /api/vehicles by account type (the #1676 regression: a replay account took the Postgres path and lost `make`, and its
// pack size changed). The body must be byte for byte what the old path gives, for every kind of account.

// listingClient answers the vehicle list the way Rivian's list (or a replay feed) would.
type listingClient struct {
	rivian.Client
	vs    []rivian.Vehicle
	calls int
}

func (l *listingClient) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	l.calls++
	return append([]rivian.Vehicle(nil), l.vs...), nil
}

func goldenVehicle() rivian.Vehicle {
	return rivian.Vehicle{
		ID: "rv-gold-1", VIN: "VIN-GOLD", Name: "Demo R1S", Model: "R1S", ModelYear: 2024, Make: "Rivian", TrimID: "LRG-DM-PRFM", TrimName: "Dual Performance",
		PackKWh: 131, ImageURL: "https://img.test/gold.png",
		Images: []rivian.VehicleImage{{VehicleID: "rv-gold-1", URL: "https://img.test/gold.png", Placement: "side-exterior-3qfront-driver"}},
	}
}

func goldenGet(t *testing.T, ctx context.Context, uid uuid.UUID, h func() http.HandlerFunc) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
	h()(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestVehiclesBodyIsUnchangedForEveryKindOfAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	v := goldenVehicle()
	notLinked := func(context.Context) (bool, bool, bool) { return false, false, true }
	unreadable := func(context.Context) (bool, bool, bool) { return false, false, false }

	// 1. Demo / replay: its vehicle comes from the client's list, its row (made by the old GET's own upsert, or by the replay seed) has
	// none of the owner-synced marks. Not linked, or an account registry that cannot say: the old path, whatever the rows hold.
	for name, account := range map[string]func(context.Context) (bool, bool, bool){"not linked": notLinked, "state unreadable": unreadable} {
		uid := acceptedUser(ctx, t, pool, "veh-gold-replay-"+name)
		if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1, $2, 'R1S')`, uid, v.ID+"-"+name); err != nil {
			t.Fatal(err)
		}
		oldPath := &listingClient{vs: []rivian.Vehicle{v}}
		want := goldenGet(t, ctx, uid, func() http.HandlerFunc { return handleVehicles(oldPath, nil, nil, nil, nil, nil, nil) })
		c := &listingClient{vs: []rivian.Vehicle{v}}
		got := goldenGet(t, ctx, uid, func() http.HandlerFunc {
			return handleVehicles(c, nil, pool, nil, nil, nil, &vehiclesFromDB{account: account})
		})
		if got != want || c.calls != 1 {
			t.Errorf("%s: body changed or Rivian not asked once (%d calls):\\nwant %s\\ngot  %s", name, c.calls, want, got)
		}
		// The golden facts a qa check saw go wrong: the make and the client's own pack size are there.
		if !contains(got, `"make":"Rivian"`) || !contains(got, `"pack_kwh":131`) || !contains(got, `"trim_name":"Dual Performance"`) {
			t.Errorf("%s: golden fields missing from %s", name, got)
		}
	}

	// 2. Self-hosted with no database: the old path, untouched.
	selfHosted := &listingClient{vs: []rivian.Vehicle{v}}
	if body := goldenGet(t, ctx, uuid.New(), func() http.HandlerFunc { return handleVehicles(selfHosted, nil, nil, nil, nil, nil, nil) }); !contains(body, `"make":"Rivian"`) || selfHosted.calls != 1 {
		t.Errorf("self-hosted: %s, %d calls", body, selfHosted.calls)
	}

	// 3. Connected and synced: the Postgres path gives the same bytes the old path gave for the same vehicle, with no Rivian call.
	uid := acceptedUser(ctx, t, pool, "veh-gold-connected")
	stored := v
	stored.InfoSynced = true
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: v.ID, VIN: v.VIN, Name: v.Name, Model: v.Model, ModelYear: v.ModelYear, Trim: v.TrimID,
		PackKWh: v.PackKWh, ImageURL: v.ImageURL, Info: stored.StoredInfo()}); err != nil {
		t.Fatal(err)
	}
	connectedWant := &listingClient{vs: []rivian.Vehicle{func() rivian.Vehicle {
		w := v
		w.PackKWh = rivian.InferPackKWh(v.Model, v.TrimID, v.ModelYear)
		return w
	}()}}
	want := goldenGet(t, ctx, uid, func() http.HandlerFunc { return handleVehicles(connectedWant, nil, nil, nil, nil, nil, nil) })
	c := &listingClient{}
	got := goldenGet(t, ctx, uid, func() http.HandlerFunc {
		return handleVehicles(c, nil, pool, nil, nil, nil, &vehiclesFromDB{account: linkedAccount})
	})
	if got != want || c.calls != 0 {
		t.Errorf("connected: body differs from the old path or Rivian was asked (%d calls):\\nwant %s\\ngot  %s", c.calls, want, got)
	}

	// 4. Connected but not yet synced by the owner (a row from before the gallery was stored): the old path answers, asking Rivian once, and
	// the next list inside the window does not ask again.
	uid2 := acceptedUser(ctx, t, pool, "veh-gold-unsynced")
	if err := db.UpsertVehicleFromSync(ctx, pool, uid2, db.VehicleSync{RivianID: v.ID, VIN: v.VIN, Name: v.Name, Model: v.Model, ModelYear: v.ModelYear, Trim: v.TrimID, PackKWh: v.PackKWh}); err != nil {
		t.Fatal(err)
	}
	gate := newVehicleFallbackGate(nil, "", nil, 5*time.Minute, nil)
	c2 := &listingClient{vs: []rivian.Vehicle{v}}
	first := goldenGet(t, ctx, uid2, func() http.HandlerFunc {
		return handleVehicles(c2, nil, pool, nil, nil, nil, &vehiclesFromDB{account: linkedAccount, gate: gate})
	})
	if c2.calls != 1 || !contains(first, `"make":"Rivian"`) || !contains(first, `"pack_kwh":131`) {
		t.Errorf("unsynced connected row: %d calls, body %s; want the old path's body from one Rivian call", c2.calls, first)
	}
}
