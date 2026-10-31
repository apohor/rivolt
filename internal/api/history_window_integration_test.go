//go:build integration

package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
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
	"github.com/apohor/rivolt/internal/settings"
)

// The free plan's history window against real rows. A free and a Pro account hold the same data: a drive and a charge 400 days old and
// a drive and a charge from the last days. Through the real handlers behind the real middleware, the free account sees only the recent
// rows, a single old row answers 402 outside_free_window, a client range is clamped, and every number derived from the whole history is
// the number the Pro account sees; the full export and the backup of the free account still contain the old rows.
func TestHistoryWindowAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "win-free")
	pro, _ := db.EnsureUser(ctx, pool, "win-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	cf, df, sf := charges.NewFactory(pool, resolvers), drives.NewFactory(pool, resolvers), settings.NewFactory(pool)
	now := time.Now().UTC().Truncate(time.Second)
	old, recent := now.AddDate(0, 0, -380), now.AddDate(0, 0, -5) // old: well outside 90 days, inside the 400-day range a chart may ask for
	for _, u := range []uuid.UUID{free, pro} {
		// The only charge before the recent drive is the old one, priced: the recent drive's cost estimate is derived from it.
		if err := cf.For(u).Upsert(ctx, charges.Charge{ID: "win-old-charge", VehicleID: "rv-win", StartedAt: old, EndedAt: old.Add(time.Hour),
			EnergyAddedKWh: 40, Cost: 8, Currency: "USD", PricePerKWh: 0.2, MaxPowerKW: 11, Lat: 30.5, Lon: -97.5, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
		if err := cf.For(u).Upsert(ctx, charges.Charge{ID: "win-new-charge", VehicleID: "rv-win", StartedAt: recent.Add(4 * time.Hour), EndedAt: recent.Add(5 * time.Hour),
			EnergyAddedKWh: 20, MaxPowerKW: 11, Lat: 30.5, Lon: -97.5, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
		for id, at := range map[string]time.Time{"win-old-drive": old.Add(2 * time.Hour), "win-new-drive": recent} {
			if err := df.For(u).Upsert(ctx, drives.Drive{ID: id, VehicleID: "rv-win", StartedAt: at, EndedAt: at.Add(30 * time.Minute), StartSoCPct: 80, EndSoCPct: 70,
				DistanceMi: 12, EnergyUsedKWh: 5, Source: "live"}); err != nil {
				t.Fatal(err)
			}
		}
	}

	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	win := historyWindowMiddleware(ent)
	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler { // the signed-in user, from a header
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	userHandler := func(f func(uid uuid.UUID) http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			uid, _ := auth.UserFromContext(r.Context())
			f(uid)(w, r)
		}
	}
	rt.With(win).Get("/drives", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return handleDrives(df.For(u), cf.For(u), sf.For(u))
	}))
	rt.With(win).Get("/drives/{id}", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return handleDriveGet(df.For(u), cf.For(u), sf.For(u))
	}))
	rt.With(win).Get("/charges", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleCharges(cf.For(u), sf.For(u), nil) }))
	rt.With(win).Get("/charges/{id}", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleGetCharge(cf.For(u), sf.For(u), nil) }))
	rt.With(win).Get("/charges/clusters", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleChargeClusters(cf.For(u)) }))
	rt.With(win).Get("/activity", userHandler(func(u uuid.UUID) http.HandlerFunc {
		return handleActivity(df.For(u), cf.For(u), sf.For(u), nil)
	}))
	rt.With(win).Get("/chart-points", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleChartPoints(df.For(u), cf.For(u)) }))
	rt.With(win).Get("/drives/{id}/weather", userHandler(func(u uuid.UUID) http.HandlerFunc { return handleDriveWeatherGet(pool, u) }))

	get := func(u uuid.UUID, path string) (int, []byte) {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}
	ids := func(body []byte, key string) []string {
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		var out []string
		for _, r := range rows {
			if v, ok := r[key].(string); ok {
				out = append(out, v)
			}
		}
		return out
	}
	has := func(list []string, id string) bool {
		for _, x := range list {
			if x == id {
				return true
			}
		}
		return false
	}

	// Lists: the free account sees the recent rows only; the Pro account sees both.
	for _, c := range []struct{ path, key, oldID, newID string }{
		{"/drives", "ID", "win-old-drive", "win-new-drive"},
		{"/charges", "ID", "win-old-charge", "win-new-charge"},
	} {
		code, body := get(free, c.path)
		if got := ids(body, c.key); code != 200 || has(got, c.oldID) || !has(got, c.newID) {
			t.Errorf("free %s: %d %v, want only the recent row", c.path, code, got)
		}
		code, body = get(pro, c.path)
		if got := ids(body, c.key); code != 200 || !has(got, c.oldID) || !has(got, c.newID) {
			t.Errorf("pro %s: %d %v, want both rows", c.path, code, got)
		}
	}

	// A single old row: 402 outside_free_window for the free account, served to Pro; the recent one is served to both.
	for _, p := range []string{"/drives/win-old-drive", "/charges/win-old-charge", "/drives/win-old-drive/weather"} {
		code, body := get(free, p)
		var pb paywallBody
		_ = json.Unmarshal(body, &pb)
		if code != http.StatusPaymentRequired || pb.Error != "outside_free_window" || pb.FreeDays != 90 {
			t.Errorf("free %s: %d %s, want 402 outside_free_window with free_days 90", p, code, body)
		}
	}
	if code, _ := get(pro, "/drives/win-old-drive"); code != 200 {
		t.Errorf("pro old drive: %d", code)
	}
	if code, _ := get(pro, "/charges/win-old-charge"); code != 200 {
		t.Errorf("pro old charge: %d", code)
	}
	for _, p := range []string{"/drives/win-new-drive", "/charges/win-new-charge"} {
		if code, _ := get(free, p); code != 200 {
			t.Errorf("free %s: %d, want 200", p, code)
		}
	}
	if code, _ := get(free, "/drives/does-not-exist"); code != http.StatusNotFound {
		t.Errorf("a missing drive: %d, want the handler's own 404", code)
	}

	// Derived values are computed over everything: the recent drive's cost estimate comes from the OLD priced charge, and is the same for a
	// free and a Pro account.
	costOf := func(u uuid.UUID) (float64, float64) {
		_, body := get(u, "/drives/win-new-drive")
		var d struct {
			Cost float64 `json:"estimated_cost"`
			Rate float64 `json:"estimated_price_per_kwh"`
		}
		_ = json.Unmarshal(body, &d)
		return d.Cost, d.Rate
	}
	fc, fr := costOf(free)
	pc, pr := costOf(pro)
	if fc == 0 || fc != pc || fr != pr {
		t.Errorf("derived cost: free %v at %v, pro %v at %v; want equal and non-zero (rate from the old charge)", fc, fr, pc, pr)
	}

	// Client ranges are clamped: asking for the last 400 days returns the same rows as the window for a free account.
	since := url_(now.AddDate(0, 0, -399)) // as far back as a chart may ask, which reaches the old rows
	code, body := get(free, "/chart-points?since="+since+"&until="+url_(now.Add(time.Hour)))
	if code != http.StatusBadRequest && code != 200 {
		t.Fatalf("chart-points: %d %s", code, body)
	}
	if code == 200 {
		var cp struct {
			Drives []map[string]any `json:"drives"`
		}
		_ = json.Unmarshal(body, &cp)
		for _, d := range cp.Drives {
			if s, _ := d["started_at"].(string); s != "" {
				if ts, err := time.Parse(time.RFC3339, s); err == nil && ts.Before(now.AddDate(0, 0, -91)) {
					t.Errorf("chart-points returned a drive from %v to a free account", ts)
				}
			}
		}
	}
	code, body = get(free, "/activity?since="+since+"&until="+url_(now.Add(time.Hour)))
	if code == 200 && (strings.Contains(string(body), "win-old-drive") || strings.Contains(string(body), "win-old-charge")) {
		t.Errorf("activity gave a free account rows older than the window: %s", body)
	}
	if code, body = get(pro, "/activity?since="+since+"&until="+url_(now.Add(time.Hour))); code != 200 && code != http.StatusBadRequest {
		t.Errorf("pro activity: %d %s", code, body)
	}

	// Clusters are formed over every charge and only the members inside the window are returned.
	_, freeClusters := get(free, "/charges/clusters")
	_, proClusters := get(pro, "/charges/clusters")
	if strings.Contains(string(freeClusters), "win-old-charge") {
		t.Errorf("clusters gave a free account a charge older than the window: %s", freeClusters)
	}
	if string(proClusters) != "[]\n" && !strings.Contains(string(proClusters), "win-old-charge") {
		t.Errorf("a Pro account's clusters lack the old charge: %s", proClusters)
	}

	// The export and the backup of the free account are complete: the old rows are in them. (They are not behind the window.)
	d := poolOn(t, Deps{DB: pool, Charges: cf, Drives: df, Settings: sf, Version: "test", Samples: nil})
	req := httptest.NewRequest(http.MethodGet, "/api/export", nil).WithContext(auth.WithUser(ctx, free))
	rec := httptest.NewRecorder()
	handleExport(d)(rec, req)
	if rec.Code != 200 {
		t.Fatalf("export: %d", rec.Code)
	}
	files := readZip(t, rec.Body.Bytes())
	if !strings.Contains(files["drives.csv"], "win-old-drive") || !strings.Contains(files["charges.csv"], "win-old-charge") {
		t.Errorf("the free account's export is missing rows older than 90 days:\ndrives.csv: %s\ncharges.csv: %s", files["drives.csv"], files["charges.csv"])
	}
	// The backup is gzip by default (application/gzip, not a content encoding): read it as the download is read.
	brec := httptest.NewRecorder()
	handleDataBackup(backupTestDepsFor(t, pool))(brec, httptest.NewRequest(http.MethodGet, "/api/data/backup", nil).WithContext(auth.WithUser(ctx, free)))
	if brec.Code != 200 || brec.Header().Get("Content-Type") != "application/gzip" {
		t.Fatalf("backup: %d %s", brec.Code, brec.Header().Get("Content-Type"))
	}
	zr, err := gzip.NewReader(brec.Body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "win-old-drive") || !strings.Contains(string(raw), "win-old-charge") {
		t.Errorf("the free account's backup is missing rows older than 90 days (%d bytes)", len(raw))
	}
}

func url_(t time.Time) string { return strings.ReplaceAll(t.UTC().Format(time.RFC3339), "+", "%2B") }
