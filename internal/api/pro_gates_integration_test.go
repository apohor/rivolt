//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
	"github.com/apohor/rivolt/internal/trips"
)

// The free plan's feature gates against real rows (Pro 18): saved trips, the planner, vehicles. A free and a Pro account hold the same
// data; through the real handlers behind the real middleware the free account is refused with 402 pro_required where the plan says so,
// and nothing is ever deleted.
func TestProFeatureGatesAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "gate-free")
	pro, _ := db.EnsureUser(ctx, pool, "gate-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	resolvers := db.NewVehicleResolverFactory(pool)
	cf, df, sf, tf := charges.NewFactory(pool, resolvers), drives.NewFactory(pool, resolvers), settings.NewFactory(pool), trips.NewFactory(pool)
	pv := &proVehicles{db: pool, settings: sf, ent: ent}

	// Two vehicles each; rv-test-b has the latest drive, so it is the default primary.
	now := time.Now().UTC().Truncate(time.Second)
	for _, u := range []uuid.UUID{free, pro} {
		for i, veh := range []string{"rv-test-a", "rv-test-b"} {
			at := now.Add(-time.Duration(48-24*i) * time.Hour)
			if err := df.For(u).Upsert(ctx, drives.Drive{ID: "gate-drive-" + veh, VehicleID: veh, StartedAt: at, EndedAt: at.Add(30 * time.Minute),
				StartSoCPct: 80, EndSoCPct: 70, DistanceMi: 12, EnergyUsedKWh: 5, Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, u := range []uuid.UUID{free, pro} {
		for i, veh := range []string{"rv-test-a", "rv-test-b"} {
			at := now.Add(-time.Duration(40-10*i) * time.Hour)
			if err := cf.For(u).Upsert(ctx, charges.Charge{ID: "gate-charge-" + veh, VehicleID: veh, StartedAt: at, EndedAt: at.Add(time.Hour),
				EnergyAddedKWh: 10, MaxPowerKW: 11, FinalState: "charging_complete", Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
		// A year-old drive of the hidden vehicle, for delete-range.
		old := now.AddDate(0, 0, -380)
		if err := df.For(u).Upsert(ctx, drives.Drive{ID: "gate-old-drive-rv-test-a", VehicleID: "rv-test-a", StartedAt: old, EndedAt: old.Add(time.Hour), DistanceMi: 5, Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}

	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	as := func(f func(uid uuid.UUID, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			uid, _ := auth.UserFromContext(r.Context())
			f(uid, w, r)
		}
	}
	rt.Get("/trips/saved", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) {
		handleSavedTripsList(tf.For(u), ent, u)(w, r)
	}))
	rt.Post("/trips/saved", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) {
		handleSavedTripCreate(tf.For(u), ent, u)(w, r)
	}))
	rt.Put("/trips/saved/{id}", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) {
		handleSavedTripUpdate(tf.For(u), ent, u)(w, r)
	}))
	rt.Delete("/trips/saved/{id}", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) { handleSavedTripDelete(tf.For(u))(w, r) }))
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	rt.With(multidayProMiddleware(ent)).Post("/trips/plan-multiday", ok)
	rt.With(proOnlyMiddleware(ent)).Get("/trips/saved/{id}/actual", ok)
	rt.With(freeVehicleMiddleware(pv)).Get("/drives", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) {
		handleDrives(df.For(u), cf.For(u), sf.For(u))(w, r)
	}))
	rt.With(freeVehicleMiddleware(pv)).Get("/activity", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) {
		handleActivity(df.For(u), cf.For(u), sf.For(u), nil)(w, r)
	}))
	rt.Delete("/charges/{id}", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) { handleDeleteCharge(cf.For(u))(w, r) }))
	rt.Post("/data/delete-range", handleDataDeleteRange(Deps{Drives: df, Charges: cf, Samples: samples.NewFactory(pool, resolvers), DB: pool}))
	rt.With(rowVehicleMiddleware(pv, "drives")).Get("/drives/{id}", ok)
	rt.With(vehicleGateMiddleware(pv)).Get("/state/{vehicleID}", ok)
	rt.Put("/settings/primary-vehicle", as(func(u uuid.UUID, w http.ResponseWriter, r *http.Request) { handlePrimaryVehicle(pv)(u, w, r) }))

	call := func(u uuid.UUID, method, path, body string) (int, []byte) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	wantCode := func(what string, got, want int, body []byte) {
		t.Helper()
		if got != want {
			t.Errorf("%s: %d %s, want %d", what, got, body, want)
		}
	}
	wantPro := func(what string, got int, body []byte) {
		t.Helper()
		if got != http.StatusPaymentRequired || !strings.Contains(string(body), `"pro_required"`) {
			t.Errorf("%s: %d %s, want 402 pro_required", what, got, body)
		}
	}

	// Saved trips: 3 for a free account, the rest of a lapsed account's trips kept read-only; Pro is not limited here.
	create := func(u uuid.UUID, name string) (int, []byte) {
		return call(u, http.MethodPost, "/trips/saved", fmt.Sprintf(`{"name":%q,"inputs":{"o":"x"}}`, name))
	}
	for i := 1; i <= FreeSavedTrips; i++ {
		code, b := create(free, fmt.Sprintf("trip %d", i))
		wantCode("free save", code, http.StatusCreated, b)
		time.Sleep(10 * time.Millisecond) // distinct updated_at
	}
	code, b := create(free, "trip 4")
	wantPro("free save over the cap", code, b)
	for i := 1; i <= FreeSavedTrips+2; i++ {
		code, b := create(pro, fmt.Sprintf("trip %d", i))
		wantCode("pro save", code, http.StatusCreated, b)
		time.Sleep(10 * time.Millisecond)
	}
	// The same five trips, as an account whose Pro has lapsed sees them: give the Pro account's rows to the free account.
	if _, err := pool.ExecContext(ctx, `UPDATE saved_trips SET user_id = $1 WHERE user_id = $2 AND name IN ('trip 4','trip 5')`, free, pro); err != nil {
		t.Fatal(err)
	}
	code, b = call(free, http.MethodGet, "/trips/saved", "")
	wantCode("list", code, http.StatusOK, b)
	var list []trips.SavedTrip
	if err := json.Unmarshal(b, &list); err != nil || len(list) != FreeSavedTrips+2 {
		t.Fatalf("list: %v %s", err, b)
	}
	ro := map[string]bool{}
	for _, tr := range list {
		ro[tr.Name] = tr.ReadOnly
	}
	n := 0
	for _, v := range ro {
		if v {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d trips read-only, want 2 (all but the 3 most recently updated): %v", n, ro)
	}
	var readOnlyID, editableID string
	for _, tr := range list {
		if tr.ReadOnly {
			readOnlyID = tr.ID.String()
		} else {
			editableID = tr.ID.String()
		}
	}
	put := func(id, name string) (int, []byte) {
		return call(free, http.MethodPut, "/trips/saved/"+id, fmt.Sprintf(`{"name":%q,"inputs":{"o":"y"}}`, name))
	}
	code, b = put(readOnlyID, "renamed")
	wantPro("edit a read-only trip", code, b)
	code, b = put(editableID, "renamed editable")
	wantCode("edit a recent trip", code, http.StatusOK, b)
	code, b = call(free, http.MethodDelete, "/trips/saved/"+readOnlyID, "")
	wantCode("delete a read-only trip", code, http.StatusNoContent, b)
	var kept int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM saved_trips WHERE user_id = $1`, free).Scan(&kept); err != nil || kept != FreeSavedTrips+1 {
		t.Errorf("trips kept: %d %v, want %d (only the one deleted on request is gone)", kept, err, FreeSavedTrips+1)
	}
	// Deleting freed no slot yet (still over the cap), so a new save is still refused.
	code, b = create(free, "trip 6")
	wantPro("save while still over the cap", code, b)

	// Plan-vs-actual and the planner.
	id := uuid.NewString()
	code, b = call(free, http.MethodGet, "/trips/saved/"+id+"/actual", "")
	wantPro("plan-vs-actual, free", code, b)
	code, b = call(pro, http.MethodGet, "/trips/saved/"+id+"/actual", "")
	wantCode("plan-vs-actual, pro", code, http.StatusNoContent, b)
	oneDay := `{"days":[{"end":{"lat":1,"lon":2}}]}`
	code, b = call(free, http.MethodPost, "/trips/plan-multiday", oneDay)
	wantCode("one-day plan, free", code, http.StatusNoContent, b)
	for name, body := range map[string]string{
		"two days":     `{"days":[{"end":{}},{"end":{}}]}`,
		"an overnight": `{"days":[{"end":{},"overnight":{"l2_kw":7.2}}]}`,
		"re-plan":      `{"origin_from_vehicle":true,"days":[{"end":{}}]}`,
	} {
		code, b = call(free, http.MethodPost, "/trips/plan-multiday", body)
		wantPro(name+", free", code, b)
		code, b = call(pro, http.MethodPost, "/trips/plan-multiday", body)
		wantCode(name+", pro", code, http.StatusNoContent, b)
	}

	// Vehicles: the default primary is the one with the latest drive; the other is Pro; the choice is stored and can be changed.
	rows := func(u uuid.UUID) []string {
		code, b := call(u, http.MethodGet, "/drives", "")
		wantCode("drives", code, http.StatusOK, b)
		var ds []struct {
			ID string
		}
		_ = json.Unmarshal(b, &ds)
		var out []string
		for _, d := range ds {
			out = append(out, d.ID)
		}
		return out
	}
	if got := rows(pro); len(got) != 3 {
		t.Errorf("pro sees %v, want every drive of both vehicles (3)", got)
	}
	if got := rows(free); len(got) != 1 || got[0] != "gate-drive-rv-test-b" {
		t.Errorf("free sees %v, want only the primary vehicle's drive", got)
	}
	code, b = call(free, http.MethodGet, "/drives/gate-drive-rv-test-a", "")
	wantPro("a drive of the other vehicle", code, b)
	code, b = call(free, http.MethodGet, "/drives/gate-drive-rv-test-b", "")
	wantCode("a drive of the primary", code, http.StatusNoContent, b)
	code, b = call(free, http.MethodGet, "/state/rv-test-a", "")
	wantPro("live state of the other vehicle", code, b)
	code, b = call(free, http.MethodGet, "/state/rv-test-b", "")
	wantCode("live state of the primary", code, http.StatusNoContent, b)
	code, b = call(pro, http.MethodGet, "/state/rv-test-a", "")
	wantCode("live state, pro", code, http.StatusNoContent, b)
	code, b = call(free, http.MethodPut, "/settings/primary-vehicle", `{"vehicle_id":"rv-test-zzz"}`)
	wantCode("choose a vehicle the user does not own", code, http.StatusNotFound, b)
	code, b = call(free, http.MethodPut, "/settings/primary-vehicle", `{"vehicle_id":"rv-test-a"}`)
	wantCode("choose the primary", code, http.StatusOK, b)
	code, b = call(free, http.MethodGet, "/state/rv-test-a", "")
	wantCode("live state after choosing it", code, http.StatusNoContent, b)
	code, b = call(free, http.MethodGet, "/state/rv-test-b", "")
	wantPro("live state of the vehicle no longer primary", code, b)

	// The activity feed follows the same rule as /drives: a free account's feed has only the primary vehicle's rows.
	_, feedFree := call(free, http.MethodGet, "/activity", "")
	_, feedPro := call(pro, http.MethodGet, "/activity", "")
	if !strings.Contains(string(feedFree), "rv-test-a") || strings.Contains(string(feedFree), "rv-test-b") {
		t.Errorf("free activity feed should show only the primary (rv-test-a, chosen above): %s", feedFree)
	}
	if !strings.Contains(string(feedPro), "rv-test-a") || !strings.Contains(string(feedPro), "rv-test-b") {
		t.Errorf("pro activity feed should show both vehicles: %s", feedPro)
	}

	// Privacy exits work for the hidden vehicle (rv-test-b now) on a free account: deleting its charge and deleting a range that holds
	// its old rows are not gated. (There is no per-vehicle delete or disconnect endpoint: the exits are per row, per range and per account.)
	code, b = call(free, http.MethodDelete, "/charges/gate-charge-rv-test-b", "")
	wantCode("delete a hidden vehicle's charge", code, http.StatusNoContent, b)
	cut := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -6, 0).Format("2006-01")
	code, b = call(free, http.MethodPost, "/data/delete-range", `{"before":"`+cut+`","confirm":true}`)
	wantCode("delete-range", code, http.StatusOK, b)
	var oldLeft int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM drives WHERE user_id = $1 AND external_id = 'gate-old-drive-rv-test-a'`, free).Scan(&oldLeft); err != nil || oldLeft != 0 {
		t.Errorf("old drive of the vehicle after delete-range: %d %v, want 0", oldLeft, err)
	}

	// Nothing was deleted: both drives of both accounts are still stored.
	var stored int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM drives WHERE user_id IN ($1, $2) AND external_id IN ('gate-drive-rv-test-a','gate-drive-rv-test-b')`, free, pro).Scan(&stored); err != nil || stored != 4 {
		t.Errorf("drives stored: %d %v, want 4 (only what the user deleted is gone)", stored, err)
	}

	// Plan-vs-actual linking is an automatic write: it must not bump a saved trip's updated_at, or the 3 editable trips would shift.
	var tripID uuid.UUID
	var before, after time.Time
	if err := pool.QueryRowContext(ctx, `SELECT id, updated_at FROM saved_trips WHERE user_id = $1 ORDER BY updated_at LIMIT 1`, pro).Scan(&tripID, &before); err != nil {
		t.Fatal(err)
	}
	links := pgPlanLinks{db: pool}
	if err := links.Link(ctx, pro, tripID, planLink{Day: 0, TripID: "gate-link", Status: "linked", StartedAt: now, EndedAt: now.Add(time.Hour), Snapshot: planSnapshot{Inputs: json.RawMessage(`{}`), Plan: json.RawMessage(`{}`)}}); err != nil {
		t.Fatal(err)
	}
	if err := links.Unlink(ctx, pro, tripID, planLink{Day: 0, TripID: "gate-link", StartedAt: now, EndedAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT updated_at FROM saved_trips WHERE id = $1`, tripID).Scan(&after); err != nil || !after.Equal(before) {
		t.Errorf("linking changed the saved trip's updated_at: %v -> %v (%v)", before, after, err)
	}
}
