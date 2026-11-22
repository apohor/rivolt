//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/demo"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/histwin"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// The preview sample truck's older history through the real writer and the real history window: a free account on the truck sees only the
// current month and the twelve before it, a Pro account sees everything back to the first generated drive, the history ends before the
// recorded rows without overlapping them, a second run changes nothing, and the demo account and a prod environment are refused.
func TestSampleHistoryFreeWindowAndPro(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "hist-free")
	pro, _ := db.EnsureUser(ctx, pool, "hist-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	cf, df, sf := charges.NewFactory(pool, resolvers), drives.NewFactory(pool, resolvers), settings.NewFactory(pool)
	now := time.Now().UTC().Truncate(time.Second)
	firstRecorded := now.AddDate(0, 0, -60)
	for _, u := range []uuid.UUID{free, pro} {
		veh := rivian.DemoVehicleID(u)
		if err := df.For(u).Upsert(ctx, drives.Drive{ID: "rec-drive", VehicleID: veh, StartedAt: firstRecorded, EndedAt: firstRecorded.Add(30 * time.Minute),
			StartSoCPct: 80, EndSoCPct: 75, DistanceMi: 12, EnergyUsedKWh: 6, Source: "live"}); err != nil {
			t.Fatal(err)
		}
		if err := cf.For(u).Upsert(ctx, charges.Charge{ID: "rec-charge", VehicleID: veh, StartedAt: firstRecorded.Add(3 * time.Hour), EndedAt: firstRecorded.Add(4 * time.Hour),
			EnergyAddedKWh: 20, MaxPowerKW: 7, FinalState: "charging_complete", Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	preview := func(k string) string {
		return map[string]string{"RIVOLT_SIM_CONTROLS": "true", "RIVOLT_RIVIAN_LIVE_FORBIDDEN": "true"}[k]
	}
	w := &demo.SampleHistoryWriter{DB: pool, Drives: df, Charges: cf, Getenv: preview}
	var first demo.SampleHistoryResult
	for _, u := range []uuid.UUID{free, pro} {
		res, err := w.Run(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		first = res
	}
	if first.Drives < 800 || first.Charges < 150 || first.Weather != int64(first.Drives) {
		t.Errorf("wrote %+v", first)
	}
	if first.Before.After(firstRecorded) {
		t.Errorf("the history ends at %v, after the first recorded row %v", first.Before, firstRecorded)
	}
	count := func(q string, u uuid.UUID) (n int) {
		if err := pool.QueryRowContext(ctx, q, u).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count(`SELECT count(*) FROM drives WHERE user_id = $1`, pro)
	if _, err := w.Run(ctx, pro); err != nil {
		t.Fatal(err)
	}
	if after := count(`SELECT count(*) FROM drives WHERE user_id = $1`, pro); after != before || before != first.Drives+1 {
		t.Errorf("drives %d then %d after a second run, want %d", before, after, first.Drives+1)
	}
	if n := count(`SELECT count(*) FROM drive_weather WHERE user_id = $1`, pro); n != first.Drives {
		t.Errorf("%d weather rows, want %d", n, first.Drives)
	}

	// Refusals: the demo account, and a prod environment (no flags) for a real account.
	if _, err := w.Run(ctx, demo.UserID()); err == nil {
		t.Error("the demo account was written")
	}
	prod := &demo.SampleHistoryWriter{DB: pool, Drives: df, Charges: cf, Getenv: func(string) string { return "" }}
	stranger, _ := db.EnsureUser(ctx, pool, "hist-prod")
	if _, err := prod.Run(ctx, stranger); err == nil || count(`SELECT count(*) FROM drives WHERE user_id = $1`, stranger) != 0 {
		t.Errorf("prod environment: err = %v", err)
	}
	if _, err := w.Run(ctx, stranger); err == nil {
		t.Error("an account with no recorded history was written")
	}

	// The free window against these rows.
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(wr http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(wr, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	rt.With(historyWindowMiddleware(ent)).Get("/drives", func(wr http.ResponseWriter, r *http.Request) {
		u, _ := auth.UserFromContext(r.Context())
		handleDrives(df.For(u), cf.For(u), sf.For(u))(wr, r)
	})
	type row struct {
		ID        string
		StartedAt time.Time
	}
	list := func(u uuid.UUID) []row {
		req := httptest.NewRequest(http.MethodGet, "/drives?limit=5000", nil)
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("/drives: %d %s", rec.Code, rec.Body.String())
		}
		var rows []row
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		return rows
	}
	since := histwin.Free(now, testNewAccount).Since
	proRows, freeRows := list(pro), list(free)
	if len(proRows) < first.Drives {
		t.Fatalf("pro sees %d drives, want at least the %d written", len(proRows), first.Drives)
	}
	oldest := proRows[0].StartedAt
	for _, r := range proRows {
		if r.StartedAt.Before(oldest) {
			oldest = r.StartedAt
		}
	}
	if oldest.Before(rivian.SampleHistoryAnchor) || oldest.After(rivian.SampleHistoryAnchor.AddDate(0, 0, 4)) {
		t.Errorf("pro's oldest drive is %v, want the first week of the history", oldest)
	}
	if want := now.Sub(oldest); want < 15*30*24*time.Hour {
		t.Errorf("pro sees %.0f days of history, want 15 months or more", want.Hours()/24)
	}
	if len(freeRows) >= len(proRows) {
		t.Errorf("free sees %d drives, pro %d: no window", len(freeRows), len(proRows))
	}
	for _, r := range freeRows {
		if r.StartedAt.Before(since) {
			t.Fatalf("free sees a drive from %v, before the window start %v", r.StartedAt, since)
		}
	}
}
