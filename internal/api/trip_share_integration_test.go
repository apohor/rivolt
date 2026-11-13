//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
	"github.com/apohor/rivolt/internal/tripshare"
)

// shareTestPlan is a 120 km east-west plan whose origin sits at (lat, lon) with a VIN-like entity id and a street-address name on it.
func shareTestPlan(t *testing.T, lat, lon float64) json.RawMessage {
	t.Helper()
	deg := 1 / (111320.0 * 0.809)
	var line [][2]float64
	for i := 0; i < 120; i++ {
		line = append(line, [2]float64{lat, lon + float64(i)*1000*deg})
	}
	last, mid := line[len(line)-1], line[60]
	rt := rivian.TripRoute{
		TotalDriveDistanceMeters: 119000, TotalDriveDurationSec: 5400, TotalChargingDurationSec: 900, ArrivalSoC: 22,
		Geometry: maps.EncodePolyline6(line), GeometryPrecision: 6,
		Waypoints: []rivian.PlannedWaypoint{
			{WaypointType: "origin", Source: "origin", Latitude: lat, Longitude: lon, Name: "742 Evergreen Terrace", EntityID: "ent-origin", DepartureSoC: 90},
			{WaypointType: "charger", Source: "planner", Name: "Public Charger", Latitude: mid[0], Longitude: mid[1], ArrivalSoC: 20, DepartureSoC: 70, ChargeDurationSec: 900},
			{WaypointType: "destination", Source: "destination", Latitude: last[0], Longitude: last[1], Name: "1 Destination Way", EntityID: "ent-dest"},
		},
	}
	b, err := json.Marshal(map[string]any{"Routes": []rivian.TripRoute{rt}, "costs": []any{map[string]any{"dcfc": 9.99}}, "weather_adjustment": map[string]any{"cold": true}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Create, view, list, revoke and expire a link on the real request pool: only hashes are stored, the public read works with no account, a
// revoked or expired or unknown token is the same 404, and another account can neither list nor revoke the link.
func TestTripShareLifecycleOnRequestPool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	owner, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	uidA, _ := db.EnsureUser(ctx, owner, "share-a")
	uidB, _ := db.EnsureUser(ctx, owner, "share-b")

	d := poolOn(t, Deps{DB: owner, Trips: trips.NewFactory(owner), Settings: settings.NewFactory(owner)})
	a := newTripShareAPI(d)

	// 36.0123,-115.0371 with a home pin on the origin.
	hs := d.Settings.For(uidA)
	if err := settings.SetHomeLocation(auth.WithUser(ctx, uidA), hs, settings.HomeLocation{Set: true, Latitude: 36.0123, Longitude: -115.0371, Label: "Home", Source: "coordinates"}); err != nil {
		t.Fatal(err)
	}
	saved, err := d.Trips.For(uidA).Create(auth.WithUser(ctx, uidA), "Vegas run", json.RawMessage(`{"depart_at":"2026-11-20T15:00:00Z"}`), shareTestPlan(t, 36.0123, -115.0371), nil)
	if err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	asUser := func(uid uuid.UUID) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), uid)))
			})
		}
	}
	for _, c := range []struct {
		name string
		uid  uuid.UUID
	}{{"a", uidA}, {"b", uidB}} {
		uid := c.uid
		r.Route("/"+c.name, func(r chi.Router) {
			r.Use(asUser(uid))
			r.Post("/trips/saved/{id}/share", withUser(a.create))
			r.Get("/trips/saved/{id}/shares", withUser(a.list))
			r.Delete("/trips/shares/{id}", withUser(a.revoke))
		})
	}
	r.Post("/public", a.view)
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}

	rec := do("POST", "/a/trips/saved/"+saved.ID.String()+"/share", `{"title":"Vegas","expires_in_days":7}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if !tripshare.ValidToken(created.Token) {
		t.Fatalf("token %q", created.Token)
	}

	// Only the hash is stored; the row holds no copy of the token anywhere, snapshot included.
	var hash, snap []byte
	var cut int
	if err := owner.QueryRowContext(ctx, `SELECT token_hash, snapshot, cut_radius_m FROM trip_shares WHERE id = $1`, created.ID).Scan(&hash, &snap, &cut); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, tripshare.HashToken(created.Token)) || bytes.Contains(snap, []byte(created.Token)) {
		t.Fatal("stored hash mismatch or token in snapshot")
	}
	if cut < 3000 || cut > 7000 {
		t.Fatalf("cut radius %d", cut)
	}
	for _, bad := range []string{"742 Evergreen", "1 Destination", "ent-origin", "ent-dest", "9.99", "cold", "Vegas run"} {
		if bytes.Contains(snap, []byte(bad)) {
			t.Fatalf("snapshot contains %q", bad)
		}
	}

	view := func(token string) *httptest.ResponseRecorder {
		return do("POST", "/public", `{"token":"`+token+`"}`)
	}
	pub := view(created.Token)
	if pub.Code != http.StatusOK {
		t.Fatalf("view: %d %s", pub.Code, pub.Body.String())
	}
	var got tripshare.Snapshot
	if err := json.Unmarshal(pub.Body.Bytes(), &got); err != nil || got.Title != "Vegas" || len(got.Route.Segments) == 0 || got.DepartAt != "2026-11-20T15:00:00Z" {
		t.Fatalf("snapshot: %v %+v", err, got)
	}
	// The home pin is on the origin: nothing of the line may come within the home radius plus 3 km of it.
	for _, seg := range got.Route.Segments {
		for _, p := range maps.DecodePolyline6(seg) {
			if dm := tripshare.DistM(36.0123, -115.0371, p[0], p[1]); dm < rivian.HomeRadiusM+3000 {
				t.Fatalf("line within %.0f m of home", dm)
			}
		}
	}

	// Other accounts see and revoke nothing of A's.
	if rec := do("GET", "/b/trips/saved/"+saved.ID.String()+"/shares", ""); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), created.ID) {
		t.Fatalf("b lists: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do("DELETE", "/b/trips/shares/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("b revokes a's link: %d", rec.Code)
	}
	if view(created.Token).Code != http.StatusOK {
		t.Fatal("a's link died when b tried to revoke it")
	}
	if rec := do("POST", "/b/trips/saved/"+saved.ID.String()+"/share", `{}`); rec.Code != http.StatusNotFound {
		t.Fatalf("b shares a's trip: %d", rec.Code)
	}

	// The request role cannot call the public lookup function.
	if _, err := d.DB.ExecContext(auth.WithUser(ctx, uidA), `SELECT trip_share_get($1)`, hash); err == nil {
		t.Fatal("request role can execute trip_share_get")
	}

	// Revoke, twice; then the same 404 as an unknown token.
	if rec := do("DELETE", "/a/trips/shares/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec := do("DELETE", "/a/trips/shares/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke again: %d", rec.Code)
	}
	revoked := view(created.Token)
	unknown := view("AAAAAAAAAAAAAAAAAAAAAA")
	if revoked.Code != http.StatusNotFound || unknown.Code != http.StatusNotFound || revoked.Body.String() != unknown.Body.String() {
		t.Fatalf("revoked %d %q, unknown %d %q", revoked.Code, revoked.Body.String(), unknown.Code, unknown.Body.String())
	}

	// Expiry: a link past its time reads as unknown.
	rec = do("POST", "/a/trips/saved/"+saved.ID.String()+"/share", `{"expires_in_days":1}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if view(created.Token).Code != http.StatusOK {
		t.Fatal("fresh link not readable")
	}
	if _, err := owner.ExecContext(ctx, `UPDATE trip_shares SET expires_at = now() - interval '1 minute' WHERE id = $1`, created.ID); err != nil {
		t.Fatal(err)
	}
	if view(created.Token).Code != http.StatusNotFound {
		t.Fatal("expired link still readable")
	}

	// Sweep removes ended links after 30 days, not before.
	if n, _ := a.store.Sweep(ctx); n != 0 {
		t.Fatalf("swept %d fresh rows", n)
	}
	if _, err := owner.ExecContext(ctx, `UPDATE trip_shares SET revoked_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := a.store.Sweep(ctx); err != nil || n < 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}

	// A bad request is refused without storing anything.
	for _, body := range []string{`{"expires_in_days":3}`, `{"title":"` + strings.Repeat("x", 81) + `"}`, `nope`} {
		if rec := do("POST", "/a/trips/saved/"+saved.ID.String()+"/share", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, rec.Code)
		}
	}
}

// 25 live links per account and 20 created per hour.
func TestTripShareLimitsPerAccount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	owner, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	uid, _ := db.EnsureUser(ctx, owner, "share-cap")
	d := poolOn(t, Deps{DB: owner, Trips: trips.NewFactory(owner)})
	st := &tripshare.Store{Req: d.DB, Sys: d.sysDB()}
	saved, err := d.Trips.For(uid).Create(auth.WithUser(ctx, uid), "t", json.RawMessage(`{}`), shareTestPlan(t, 36.0123, -115.0371), nil)
	if err != nil {
		t.Fatal(err)
	}
	uctx := auth.WithUser(ctx, uid)
	mk := func() error {
		_, h, _ := tripshare.NewToken()
		_, err := st.Create(uctx, uid, saved.ID, "x", h, 4000, []byte(`{}`), nil)
		return err
	}
	for i := 0; i < tripshare.MaxCreatesPerHour; i++ {
		if err := mk(); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if err := mk(); err != tripshare.ErrRate {
		t.Fatalf("21st in an hour: %v", err)
	}
	// Age them past the hour: the live-link cap is next.
	if _, err := owner.ExecContext(ctx, `UPDATE trip_shares SET created_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	for i := tripshare.MaxCreatesPerHour; i < tripshare.MaxActivePerUser; i++ {
		if err := mk(); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if err := mk(); err != tripshare.ErrCap {
		t.Fatalf("26th live link: %v", err)
	}
}
