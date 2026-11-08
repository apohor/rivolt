//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// countingRivian is a Rivian client that fails the test if anything asks it for the vehicle list.
type countingRivian struct {
	rivian.Client
	calls atomic.Int32
}

func (c *countingRivian) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	c.calls.Add(1)
	return nil, rivian.ErrNotImplemented
}

// Single-owner M3-5a (T21): a user with a Rivian account gets /api/vehicles from Postgres on every pod, with no Rivian call and no write, and
// the body is the same on each pod (the response a call to Rivian used to build: names, trim, pack, hero image, gallery, OTA, field support).
func TestVehiclesAreServedFromPostgresWithoutARivianCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid := acceptedUser(ctx, t, pool, "veh-from-db")
	info := rivian.Vehicle{TrimName: "Adventure Package", Make: "Rivian", Images: []rivian.VehicleImage{{VehicleID: "rv-db-1", URL: "https://img.test/a.png", Placement: "side-exterior-3qfront-driver"}}}
	// Row 1 stores a reported capacity (77.7) that the list must not serve: it serves the spec lookup, as the Rivian list did.
	for _, v := range []db.VehicleSync{
		{RivianID: "rv-db-1", VIN: "VIN-DB-1", Name: "Truck", Model: "R1T", ModelYear: 2024, Trim: "PKG-ADV", PackKWh: 77.7, ImageURL: "https://img.test/a.png", Info: info.StoredInfo()},
		{RivianID: "rv-db-2", Name: "Second", Model: "R1S", ModelYear: 2023, Trim: "LRG-DM-PRFM", PackKWh: 135},
	} {
		if err := db.UpsertVehicleFromSync(ctx, pool, uid, v); err != nil {
			t.Fatal(err)
		}
	}
	// A login's later prime has no gallery: it must not erase the stored one.
	if err := db.UpsertVehicleFromSync(ctx, pool, uid, db.VehicleSync{RivianID: "rv-db-1", Name: "Truck", Info: rivian.Vehicle{TrimName: "Adventure Package"}.StoredInfo()}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1, 'electrafi-abc', 'R1T')`, uid); err != nil {
		t.Fatal(err)
	}

	get := func(only string, reauth bool) (*httptest.ResponseRecorder, *countingRivian) {
		c := &countingRivian{}
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
		h := handleVehicles(c, nil, pool, nil, nil, func(context.Context) string { return only }, &vehiclesFromDB{needsReauth: func(context.Context) bool { return reauth }})
		h(rec, req)
		return rec, c
	}
	type vehicle struct {
		ID           string                 `json:"id"`
		VIN          string                 `json:"vin"`
		Name         string                 `json:"name"`
		Model        string                 `json:"model"`
		ModelYear    int                    `json:"model_year"`
		TrimID       string                 `json:"trim_id"`
		TrimName     string                 `json:"trim_name"`
		Make         string                 `json:"make"`
		PackKWh      float64                `json:"pack_kwh"`
		ImageURL     string                 `json:"image_url"`
		Images       []rivian.VehicleImage  `json:"images"`
		ProLocked    bool                   `json:"pro_locked"`
		FieldSupport map[string]string      `json:"field_support"`
		Extra        map[string]interface{} `json:"-"`
	}
	var touchedBefore time.Time
	if err := pool.QueryRowContext(ctx, `SELECT max(updated_at) FROM vehicles WHERE user_id = $1`, uid).Scan(&touchedBefore); err != nil {
		t.Fatal(err)
	}
	var first []vehicle
	var firstBody string
	for pod := 0; pod < 3; pod++ { // three pods: the same answer, none of them asks Rivian
		rec, c := get("", false)
		if rec.Code != http.StatusOK || c.calls.Load() != 0 {
			t.Fatalf("pod %d: %d, %d Rivian calls (body %s); want 200 and none", pod, rec.Code, c.calls.Load(), rec.Body.String())
		}
		if pod == 0 {
			firstBody = rec.Body.String()
			if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
				t.Fatal(err)
			}
		} else if rec.Body.String() != firstBody {
			t.Errorf("pod %d answered differently:\n%s\nvs\n%s", pod, rec.Body.String(), firstBody)
		}
	}
	if len(first) != 2 {
		t.Fatalf("%d vehicles, want 2 (the electrafi row is not a vehicle): %s", len(first), firstBody)
	}
	a, b := first[0], first[1]
	if a.ID != "rv-db-1" || a.VIN != "VIN-DB-1" || a.Name != "Truck" || a.Model != "R1T" || a.ModelYear != 2024 || a.TrimID != "PKG-ADV" || a.PackKWh != rivian.InferPackKWh("R1T", "PKG-ADV", 2024) ||
		a.TrimName != "Adventure Package" || a.Make != "Rivian" || a.ImageURL != "https://img.test/a.png" || len(a.Images) != 1 || a.Images[0].Placement != "side-exterior-3qfront-driver" {
		t.Errorf("first vehicle = %+v", a)
	}
	if b.ID != "rv-db-2" || b.Model != "R1S" || b.TrimID != "LRG-DM-PRFM" || b.PackKWh != rivian.InferPackKWh("R1S", "LRG-DM-PRFM", 2023) {
		t.Errorf("second vehicle = %+v", b)
	}
	if a.FieldSupport["power_state"] == "" {
		t.Errorf("no field support on %+v", a)
	}
	// The Pro lock still marks the extras.
	rec, _ := get("rv-db-2", false)
	var locked []vehicle
	_ = json.Unmarshal(rec.Body.Bytes(), &locked)
	if len(locked) != 2 || !locked[0].ProLocked || locked[1].ProLocked {
		t.Errorf("pro lock: %s", rec.Body.String())
	}
	// An account flagged for sign-in again still answers 401, as it did when Rivian refused the call.
	rec, c := get("", true)
	if rec.Code != http.StatusUnauthorized || c.calls.Load() != 0 {
		t.Errorf("needs reauth: %d %s, %d Rivian calls; want 401 and none", rec.Code, rec.Body.String(), c.calls.Load())
	}
	// The GET wrote nothing: no row's updated_at moved.
	var touchedAfter time.Time
	if err := pool.QueryRowContext(ctx, `SELECT max(updated_at) FROM vehicles WHERE user_id = $1`, uid).Scan(&touchedAfter); err != nil || !touchedAfter.Equal(touchedBefore) {
		t.Errorf("updated_at moved from %v to %v (%v): the GET wrote", touchedBefore, touchedAfter, err)
	}
}

// A linked account with no vehicle stored yet (the sign-in's lookup failed; the list was where that got retried) still asks Rivian, once: that
// primes the rows, and the next list is served from Postgres with no call.
func TestLinkedAccountWithNoStoredVehicleAsksRivianOnceThenStopsAsking(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid := acceptedUser(ctx, t, pool, "veh-prime-retry")
	c := &listingRivian{vs: []rivian.Vehicle{{ID: "rv-prime-1", VIN: "V1", Name: "Truck", Model: "R1T", TrimID: "PKG-ADV"}}}
	linked := func(context.Context) (bool, error) { return true, nil }
	gate := newVehicleFallbackGate(nil, "", nil, 5*time.Minute, nil)
	get := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
		handleVehicles(c, nil, pool, nil, linked, nil, &vehiclesFromDB{gate: gate})(rec, req)
		return rec.Code
	}
	if code := get(); code != http.StatusOK || c.calls.Load() != 1 {
		t.Fatalf("first list: %d, %d Rivian calls; want 200 and 1", code, c.calls.Load())
	}
	if code := get(); code != http.StatusOK || c.calls.Load() != 1 {
		t.Errorf("second list: %d, %d Rivian calls in all; want 200 and still 1 (served from the rows the first one stored)", code, c.calls.Load())
	}
}

type listingRivian struct {
	rivian.Client
	vs    []rivian.Vehicle
	calls atomic.Int32
}

func (l *listingRivian) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	l.calls.Add(1)
	return l.vs, nil
}

// Rivian keeps failing for a linked account with nothing stored: two requests inside the window make ONE Rivian call, across pods when the claim
// is in Redis (the second "pod" is a second gate on the same Redis), and the second request answers without Rivian. After the window it asks once
// more; an attempt that returned an empty list is remembered as empty.
func TestFailingFallbackIsNotRetriedWithinFiveMinutes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid := acceptedUser(ctx, t, pool, "veh-fallback-negcache")
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	var observed []string
	newPod := func() *vehicleFallbackGate {
		g := newVehicleFallbackGate(rdb, "rivolt", []byte("k"), 5*time.Minute, func(o string) { observed = append(observed, o) })
		g.now = func() time.Time { return now }
		return g
	}
	podA, podB := newPod(), newPod()
	c := &failingListing{}
	linked := func(context.Context) (bool, error) { return true, nil }
	get := func(g *vehicleFallbackGate) (int, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
		handleVehicles(c, nil, pool, nil, linked, nil, &vehiclesFromDB{gate: g})(rec, req)
		return rec.Code, rec.Header().Get("Retry-After")
	}
	if code, _ := get(podA); code < 400 || c.calls.Load() != 1 {
		t.Fatalf("first request: %d, %d Rivian calls; want an error and 1 call", code, c.calls.Load())
	}
	for i, g := range []*vehicleFallbackGate{podA, podB, podB} { // the same pod, and another pod that never saw the failure
		if code, retry := get(g); code != http.StatusServiceUnavailable || retry != "300" || c.calls.Load() != 1 {
			t.Errorf("request %d inside the window: %d (Retry-After %q), %d Rivian calls in all; want 503, 300 and still 1", i+2, code, retry, c.calls.Load())
		}
	}
	// No user id in Redis in clear: the only key is under the prefix and is not the uuid.
	for _, k := range mr.Keys() {
		if strings.Contains(k, uid.String()) {
			t.Errorf("redis key %q names the user", k)
		}
	}
	if len(mr.Keys()) != 1 {
		t.Errorf("redis keys = %v, want exactly the one claim", mr.Keys())
	}
	mr.FastForward(5*time.Minute + time.Second)
	now = now.Add(5*time.Minute + time.Second)
	if _, _ = get(podB); c.calls.Load() != 2 {
		t.Errorf("after the window: %d Rivian calls, want 2 (one more attempt)", c.calls.Load())
	}
	want := []string{"error", "skipped", "skipped", "skipped", "error"}
	if strings.Join(observed, ",") != strings.Join(want, ",") {
		t.Errorf("observed outcomes = %v, want %v", observed, want)
	}
}

type failingListing struct {
	rivian.Client
	calls atomic.Int32
}

func (f *failingListing) Vehicles(context.Context) ([]rivian.Vehicle, error) {
	f.calls.Add(1)
	return nil, errors.New("rivian is down")
}
