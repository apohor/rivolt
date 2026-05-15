package weather

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// upstreamStub returns an Open-Meteo-shaped JSON body with one
// forecast hour matching the requested date. fetchCount lets a test
// assert how many upstream calls actually happened.
func upstreamStub(t *testing.T, temp, wind, windDir float64) (*httptest.Server, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		// Pick a target time that the FetchHour parser will match —
		// the query carries start_date/end_date as YYYY-MM-DD and the
		// parser looks for the exact "YYYY-MM-DDTHH:00" entry. Echo a
		// 24-hour span and let the parser pick index 0 (fallback).
		date := r.URL.Query().Get("start_date")
		ts := []string{date + "T00:00"}
		out := map[string]any{
			"hourly": map[string]any{
				"time":                 ts,
				"temperature_2m":       []float64{temp},
				"apparent_temperature": []float64{temp},
				"wind_speed_10m":       []float64{wind},
				"wind_direction_10m":   []float64{windDir},
				"precipitation":        []float64{0},
				"relative_humidity_2m": []float64{50},
				"weather_code":         []float64{0},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestMemCache_HitsAvoidUpstream is the core value-prop: a second
// FetchHourCached for the same coarsened (lat, lon, hour) reads
// from cache instead of hitting the network.
func TestMemCache_HitsAvoidUpstream(t *testing.T) {
	srv, calls := upstreamStub(t, 10, 20, 0)
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	mc := NewMemCache(time.Hour, 100)
	// Use a past hour so FetchHour routes to the archive endpoint
	// (which is what srv.URL backs) — the forecast branch points at
	// hard-coded open-meteo.com.
	at := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Hour)

	_, _, err := mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 90, true)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	_, _, err = mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 90, true)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("upstream calls: got %d want 1 (cache miss on second read)", got)
	}
	if mc.Len() != 1 {
		t.Errorf("cache size: got %d want 1", mc.Len())
	}
}

// TestMemCache_BearingRecomputed pins the cross-bearing share: two
// reads of the same hour-bucket with different trip bearings must
// share the same upstream call AND each get their own correctly-
// projected headwind.
func TestMemCache_BearingRecomputed(t *testing.T) {
	srv, calls := upstreamStub(t, 10, 20, 270) // wind from due west, 20 kph
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	mc := NewMemCache(time.Hour, 100)
	at := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Hour)

	// East-bound trip (bearing 90) with wind from the west: wind is
	// at your back — pure tailwind, -20.
	east, _, err := mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 90, true)
	if err != nil {
		t.Fatalf("east: %v", err)
	}
	if !east.HasHeadwind || east.HeadwindKPH > -19 {
		t.Errorf("east tailwind: got %+v", east)
	}
	// West-bound trip (bearing 270) into a west wind: pure headwind, +20.
	west, _, err := mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 270, true)
	if err != nil {
		t.Fatalf("west: %v", err)
	}
	if !west.HasHeadwind || west.HeadwindKPH < 19 {
		t.Errorf("west headwind: got %+v", west)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("upstream calls: got %d want 1 (bearing should not be in the key)", got)
	}
}

// TestMemCache_CoarsenedKey: two reads at lat/lon close enough to
// share a coarse bucket must collapse onto one cache row.
func TestMemCache_CoarsenedKey(t *testing.T) {
	srv, calls := upstreamStub(t, 10, 0, 0)
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	mc := NewMemCache(time.Hour, 100)
	at := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Hour)

	// Two points within a coarse-rounding step of each other.
	_, _, _ = mc.FetchHourCached(context.Background(), client, 30.2700, -97.7400, at, 0, false)
	_, _, _ = mc.FetchHourCached(context.Background(), client, 30.2701, -97.7401, at, 0, false)
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("coarse-key collision: got %d upstream calls want 1", got)
	}
}

// TestMemCache_TTLExpiry: an entry past TTL is treated as miss.
func TestMemCache_TTLExpiry(t *testing.T) {
	srv, calls := upstreamStub(t, 10, 0, 0)
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	mc := NewMemCache(10*time.Millisecond, 100)
	at := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Hour)

	_, _, _ = mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 0, false)
	time.Sleep(25 * time.Millisecond)
	_, _, _ = mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 0, false)
	if got := atomic.LoadInt32(calls); got != 2 {
		t.Errorf("TTL expiry: got %d upstream calls want 2", got)
	}
}

// TestMemCache_CapacityEviction: exceeding max evicts the oldest.
func TestMemCache_CapacityEviction(t *testing.T) {
	srv, _ := upstreamStub(t, 10, 0, 0)
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	mc := NewMemCache(time.Hour, 2)
	base := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Hour)

	// Insert 3 distinct hours; oldest must be evicted.
	for i := 0; i < 3; i++ {
		_, _, _ = mc.FetchHourCached(context.Background(), client, 30.27, -97.74, base.Add(time.Duration(i)*time.Hour), 0, false)
		// Microsecond gap so insertedAt is distinct between rows.
		time.Sleep(time.Millisecond)
	}
	if mc.Len() != 2 {
		t.Errorf("len after eviction: got %d want 2", mc.Len())
	}
}

// TestMemCache_NilReceiver: a nil *MemCache must call through, not
// panic. The trip planner is going to wrap this once at startup; the
// rest of the codebase shouldn't have to nil-check.
func TestMemCache_NilReceiver(t *testing.T) {
	srv, calls := upstreamStub(t, 10, 0, 0)
	client := &Client{HTTP: srv.Client(), BaseURL: srv.URL}
	var mc *MemCache
	at := time.Now().Add(-200 * 24 * time.Hour).UTC().Truncate(time.Hour)
	if _, _, err := mc.FetchHourCached(context.Background(), client, 30.27, -97.74, at, 0, false); err != nil {
		t.Fatalf("nil receiver: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("nil receiver should pass through: got %d want 1", got)
	}
}
