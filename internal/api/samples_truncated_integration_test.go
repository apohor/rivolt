//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/samples"
)

// A window the store does not bucket (unbounded, or wider than 7 days) returns the
// oldest rows up to the cap; the response must say so instead of looking complete.
// A bounded window within 7 days is bucketed and flagged as downsampled, and a
// window that fits the cap is neither.
func TestSamplesSaysWhenAWideWindowWasTruncated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "samples-trunc", "samples-trunc@x.test", "samples-trunc", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1,'rv-trunc')`, uid); err != nil {
		t.Fatal(err)
	}
	store := backupTestDeps(pool).Samples.For(uid)

	end := time.Now().UTC().Truncate(time.Minute)
	start := end.Add(-10 * 24 * time.Hour)
	var batch []samples.Sample
	for i := 0; i < 400; i++ { // one row every 36 minutes over 10 days
		batch = append(batch, samples.Sample{VehicleID: "rv-trunc", At: start.Add(time.Duration(i) * 36 * time.Minute), BatteryLevelPct: float64(50 + i%40), Source: "live"})
	}
	if err := store.InsertBatch(ctx, batch); err != nil {
		t.Fatalf("seed: %v", err)
	}

	get := func(q url.Values) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handleSamples(store, nil)(w, httptest.NewRequest(http.MethodGet, "/api/samples?"+q.Encode(), nil))
		return w
	}
	rfc := func(t time.Time) string { return t.Format(time.RFC3339) }

	// 10-day window, 100-row cap: not bucketed, capped, and says so.
	w := get(url.Values{"since": {rfc(start.Add(-time.Minute))}, "until": {rfc(end.Add(time.Minute))}, "limit": {"100"}})
	if w.Code != 200 || w.Header().Get("X-Rivolt-Samples-Truncated") != "true" || w.Header().Get("X-Rivolt-Downsampled") != "" {
		t.Errorf("10-day window: code %d truncated=%q downsampled=%q, want truncated and not downsampled", w.Code, w.Header().Get("X-Rivolt-Samples-Truncated"), w.Header().Get("X-Rivolt-Downsampled"))
	}
	// Unbounded window, same cap.
	w = get(url.Values{"since": {rfc(start.Add(-time.Minute))}, "limit": {"100"}})
	if w.Header().Get("X-Rivolt-Samples-Truncated") != "true" {
		t.Errorf("unbounded window not marked truncated")
	}
	// A 10-day window that fits the cap is complete.
	w = get(url.Values{"since": {rfc(start.Add(-time.Minute))}, "until": {rfc(end.Add(time.Minute))}, "limit": {"1000"}})
	if w.Header().Get("X-Rivolt-Samples-Truncated") != "" {
		t.Errorf("a complete window was marked truncated")
	}
	// A 3-day window over the cap is bucketed, not truncated.
	w = get(url.Values{"since": {rfc(end.Add(-3 * 24 * time.Hour))}, "until": {rfc(end.Add(time.Minute))}, "limit": {"30"}})
	if w.Header().Get("X-Rivolt-Downsampled") != "true" || w.Header().Get("X-Rivolt-Samples-Truncated") != "" {
		t.Errorf("3-day window: downsampled=%q truncated=%q, want bucketed and not truncated", w.Header().Get("X-Rivolt-Downsampled"), w.Header().Get("X-Rivolt-Samples-Truncated"))
	}
}
