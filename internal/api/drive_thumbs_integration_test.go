//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/rivian"
)

// A made-up wiggly route near (10, 10), 400 points.
func thumbTestRoute() string {
	pts := make([][2]float64, 400)
	for i := range pts {
		t := float64(i) / 399
		pts[i] = [2]float64{10 + 0.2*t, 10 + 0.05*math.Sin(t*6*math.Pi)}
	}
	return rivian.EncodePolyline(pts)
}

func TestRouteThumbsAreUserScopedCappedAndSmall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "thumb-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "thumb-bob")
	if err != nil {
		t.Fatal(err)
	}
	df := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool))
	start := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	// Live drives of one vehicle may not overlap in time: each gets its own window.
	mk := func(id string, poly string, hoursAgo int) drives.Drive {
		s := start.Add(-time.Duration(hoursAgo) * time.Hour)
		return drives.Drive{ID: id, VehicleID: "rv-test-thumb", StartedAt: s, EndedAt: s.Add(40 * time.Minute), StartSoCPct: 80, EndSoCPct: 70, DistanceMi: 25, EnergyUsedKWh: 8, Source: "live", RoutePolyline: poly}
	}
	if err := df.For(alice).Upsert(ctx, mk("live_thumb_a1", thumbTestRoute(), 0)); err != nil {
		t.Fatal(err)
	}
	if err := df.For(alice).Upsert(ctx, mk("live_thumb_a2", "", 2)); err != nil { // a drive with no route
		t.Fatal(err)
	}
	if err := df.For(bob).Upsert(ctx, mk("live_thumb_b1", thumbTestRoute(), 0)); err != nil {
		t.Fatal(err)
	}

	serve := func(asAlice bool, query string) *httptest.ResponseRecorder {
		uid := alice
		if !asAlice {
			uid = bob
		}
		r := chi.NewRouter()
		r.Get("/api/drives/route-thumbs", handleRouteThumbs(df.For(uid)))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", "/api/drives/route-thumbs?"+query, nil))
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]string {
		t.Helper()
		var m map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
			t.Fatalf("body %q: %v", w.Body.String(), err)
		}
		return m
	}

	// Alice gets her own drive's thumbnail: a short polyline, and nothing for the drive that has no route or the id she does not have.
	w := serve(true, "ids=live_thumb_a1,live_thumb_a2,live_thumb_nope")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	got := decode(w)
	if len(got) != 1 || got["live_thumb_a1"] == "" {
		t.Fatalf("thumbnails = %v, want exactly live_thumb_a1", got)
	}
	if n := len(got["live_thumb_a1"]); n > 400 { // 24 points is about 24*8 characters; the full route is ~3 KB
		t.Errorf("the thumbnail is %d characters", n)
	}
	if w.Header().Get("Cache-Control") != "private, max-age=300" {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}

	// Another account's ids return nothing: Bob asks for Alice's drive and gets an empty answer, not hers and not an error that says it exists.
	if got := decode(serve(false, "ids=live_thumb_a1")); len(got) != 0 {
		t.Fatalf("Bob received %v for Alice's drive", got)
	}
	// And Bob's own is his.
	if got := decode(serve(false, "ids=live_thumb_b1,live_thumb_a1")); len(got) != 1 || got["live_thumb_b1"] == "" {
		t.Fatalf("Bob's thumbnails = %v", got)
	}

	// The caps and the plain-id rule.
	var many []string
	for i := 0; i < 61; i++ {
		many = append(many, fmt.Sprintf("id%d", i))
	}
	if w := serve(true, "ids="+strings.Join(many, ",")); w.Code != http.StatusBadRequest {
		t.Errorf("61 ids: status %d, want 400", w.Code)
	}
	if w := serve(true, "ids="+strings.Join(many[:60], ",")); w.Code != http.StatusOK {
		t.Errorf("60 ids: status %d, want 200", w.Code)
	}
	for _, bad := range []string{"ids=a%27%3B--", "ids=" + strings.Repeat("x", 129), "ids=a b"} {
		if w := serve(true, bad); w.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", bad, w.Code)
		}
	}
	// No ids: an empty object.
	if got := decode(serve(true, "")); len(got) != 0 {
		t.Errorf("no ids gave %v", got)
	}
}
