//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
)

// /api/chart-points: the list rows (a minute or more, no re-parking drives), the
// caller's own, one truck on request, inside the window, oldest first, and nothing
// but times, distance and charge percent.
func TestChartPoints(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "cp-a")
	b, _ := db.EnsureUser(ctx, pool, "cp-b")
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	cf := charges.NewFactory(pool, resolvers)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	drive := func(uid uuid.UUID, id, vid string, at time.Time, dur time.Duration, miles, s, e float64) {
		t.Helper()
		if err := df.For(uid).Upsert(ctx, drives.Drive{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(dur),
			StartSoCPct: s, EndSoCPct: e, DistanceMi: miles, EnergyUsedKWh: 3, Source: "live",
			StartLat: 30.2, StartLon: -97.7, EndLat: 30.3, EndLon: -97.8}); err != nil {
			t.Fatal(err)
		}
	}
	charge := func(uid uuid.UUID, id, vid string, at time.Time, dur time.Duration, s, e float64) {
		t.Helper()
		c := charges.Charge{ID: id, VehicleID: vid, StartedAt: at, EndedAt: at.Add(dur),
			StartSoCPct: s, EndSoCPct: e, EnergyAddedKWh: 20, FinalState: "charging_complete", Source: "live", Lat: 30.2, Lon: -97.7}
		if id == "cp-c1" { // the one charge with a known limit
			lim := 80.0
			c.ChargeLimitPct = &lim
		}
		if err := cf.For(uid).Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	drive(a, "cp-d1", "cp-rv-1", base, 30*time.Minute, 12, 80, 70)
	drive(a, "cp-d2", "cp-rv-1", base.Add(2*time.Hour), 20*time.Minute, 5, 70, 66)
	drive(a, "cp-d3", "cp-rv-2", base.Add(4*time.Hour), 25*time.Minute, 8, 90, 84) // other truck
	drive(a, "cp-reparking", "cp-rv-1", base.Add(6*time.Hour), 10*time.Minute, 0.01, 66, 66)
	drive(a, "cp-short", "cp-rv-1", base.Add(7*time.Hour), 30*time.Second, 1, 66, 65)
	drive(a, "cp-old", "cp-rv-1", base.Add(-200*time.Hour), 30*time.Minute, 9, 60, 50) // outside the window
	charge(a, "cp-c1", "cp-rv-1", base.Add(time.Hour), 40*time.Minute, 40, 80)
	charge(a, "cp-c2", "cp-rv-2", base.Add(5*time.Hour), 40*time.Minute, 30, 70)
	charge(a, "cp-cshort", "cp-rv-1", base.Add(8*time.Hour), 20*time.Second, 50, 51)
	drive(b, "cp-b1", "cp-rv-b", base.Add(time.Hour), 30*time.Minute, 15, 80, 70)
	charge(b, "cp-bc1", "cp-rv-b", base.Add(2*time.Hour), 40*time.Minute, 20, 60)

	get := func(uid uuid.UUID, q string) (int, map[string][]map[string]any, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/chart-points"+q, nil).WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		handleChartPoints(df.For(uid), cf.For(uid))(w, req)
		var out map[string][]map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out, w.Body.String()
	}
	window := "?since=" + base.Add(-24*time.Hour).Format(time.RFC3339) + "&until=" + base.Add(24*time.Hour).Format(time.RFC3339)

	code, out, raw := get(a, window)
	if code != 200 || len(out["drives"]) != 3 || len(out["charges"]) != 2 {
		t.Fatalf("all trucks: %d drives=%d charges=%d %s", code, len(out["drives"]), len(out["charges"]), raw)
	}
	var starts []string
	for _, d := range out["drives"] {
		starts = append(starts, d["started_at"].(string))
	}
	if !sort.StringsAreSorted(starts) {
		t.Errorf("drives are not oldest first: %v", starts)
	}
	// The first drive's figures, and only these keys; nothing else in the payload.
	d0 := out["drives"][0]
	if len(d0) != 5 || d0["distance_mi"].(float64) != 12 || d0["start_soc_pct"].(float64) != 80 || d0["end_soc_pct"].(float64) != 70 {
		t.Errorf("first drive = %v", d0)
	}
	if c0 := out["charges"][0]; len(c0) != 5 || c0["start_soc_pct"].(float64) != 40 || c0["end_soc_pct"].(float64) != 80 || c0["charge_limit_pct"] != 80.0 {
		t.Errorf("first charge = %v", c0)
	}
	for _, banned := range []string{"lat", "lon", "id", "cost", "place", "network", "polyline", "30.2", "-97.7", "cp-"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Errorf("the payload carries %q: %s", banned, raw)
		}
	}
	// One truck.
	if _, out, _ := get(a, window+"&vehicle_id=cp-rv-1"); len(out["drives"]) != 2 || len(out["charges"]) != 1 {
		t.Errorf("truck 1: drives=%d charges=%d", len(out["drives"]), len(out["charges"]))
	}
	if _, out, _ := get(a, window+"&vehicle_id=cp-rv-b"); len(out["drives"]) != 0 || len(out["charges"]) != 0 {
		t.Errorf("another user's truck listed rows: %v", out)
	}
	// Narrower window: only the first hours.
	narrow := "?since=" + base.Add(-time.Hour).Format(time.RFC3339) + "&until=" + base.Add(90*time.Minute).Format(time.RFC3339)
	if _, out, _ := get(a, narrow); len(out["drives"]) != 1 || len(out["charges"]) != 1 {
		t.Errorf("narrow window: drives=%d charges=%d", len(out["drives"]), len(out["charges"]))
	}
	// The other user sees only their own.
	if _, out, _ := get(b, window); len(out["drives"]) != 1 || len(out["charges"]) != 1 {
		t.Errorf("user b: drives=%d charges=%d", len(out["drives"]), len(out["charges"]))
	}
}
