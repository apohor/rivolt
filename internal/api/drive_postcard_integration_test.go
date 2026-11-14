//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/maps"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/tripshare"
)

// GET /api/drives/{id}/postcard: the route is cut on the server, the answer holds the whole-mile distance, the rounded ends and the
// trimmed line and nothing else, a drive with no route or too short a route is refused, and another account's drive is not found.
func TestDrivePostcard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	u, _ := db.EnsureUser(ctx, pool, "pc-user")
	other, _ := db.EnsureUser(ctx, pool, "pc-other")
	resolvers := db.NewVehicleResolverFactory(pool)
	df := drives.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)

	// A 59 km road east of the public sample point; the first and last points are the private ends.
	var line [][2]float64
	for i := 0; i < 60; i++ {
		line = append(line, [2]float64{34.161, -118.168 + float64(i)*0.0108})
	}
	start := time.Date(2026, 3, 5, 9, 41, 0, 0, time.UTC)
	n := 0
	put := func(id, poly string, mi float64) {
		t.Helper()
		st := start.Add(time.Duration(n) * 3 * time.Hour)
		n++
		if err := df.For(u).Upsert(ctx, drives.Drive{ID: id, VehicleID: "pc-rv-secret", StartedAt: st, EndedAt: st.Add(time.Hour), DistanceMi: mi,
			StartLat: line[0][0], StartLon: line[0][1], EndLat: line[59][0], EndLon: line[59][1], EnergyUsedKWh: 17, StartSoCPct: 80, EndSoCPct: 50,
			RoutePolyline: poly, Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	put("pc-ok", drives.EncodeRoute(line), 36.64)
	put("pc-noroute", "", 36.64)
	put("pc-short", drives.EncodeRoute(line[:4]), 2)

	call := func(id string, asOther bool) (int, string) {
		uid := u
		if asOther {
			uid = other
		}
		rt := chi.NewRouter()
		rt.Get("/drives/{id}/postcard", handleDrivePostcard(df.For(uid), sf.For(uid), uid.String()))
		req := httptest.NewRequest(http.MethodGet, "/drives/"+id+"/postcard", nil).WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		rt.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	code, body := call("pc-ok", false)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	for _, bad := range []string{"pc-rv-secret", "pc-ok", "cost", "kwh", "vehicle", "soc", "34.161"} {
		if strings.Contains(strings.ToLower(body), bad) {
			t.Errorf("postcard carries %q: %s", bad, body)
		}
	}
	var pc drivePostcard
	if err := json.Unmarshal([]byte(body), &pc); err != nil {
		t.Fatal(err)
	}
	if pc.DistanceMi != 37 || pc.Version != 1 || strings.Contains(body, "date") || strings.Contains(body, "2026") {
		t.Errorf("figures: %+v", pc)
	}
	if pc.Start.Lat == line[0][0] || pc.End.Lon == line[59][1] {
		t.Errorf("ends are not rounded: %+v %+v", pc.Start, pc.End)
	}
	cut := postcardCut(u.String(), "pc-ok")
	for _, seg := range pc.Route.Segments {
		for _, p := range maps.DecodePolyline6(seg) {
			if tripshare.DistM(p[0], p[1], line[0][0], line[0][1]) < cut-1 || tripshare.DistM(p[0], p[1], line[59][0], line[59][1]) < cut-1 {
				t.Fatalf("a kept point lies inside the cut of %.0f m", cut)
			}
		}
	}
	if _, again := call("pc-ok", false); again != body {
		t.Error("the same drive gave a different cut the second time")
	}
	if c, b := call("pc-noroute", false); c != http.StatusConflict || !strings.Contains(b, "no_route") {
		t.Errorf("no route: %d %s", c, b)
	}
	if c, b := call("pc-short", false); c != http.StatusUnprocessableEntity || !strings.Contains(b, "too_short") {
		t.Errorf("short: %d %s", c, b)
	}
	if c, _ := call("pc-ok", true); c != http.StatusNotFound {
		t.Errorf("another account's drive: %d", c)
	}
}
