package chargers

import (
	"context"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// segDist is the plain minimum distance from a point to a projected polyline, the reference for pathIndex.near.
func segDist(pp projectedPath, lat, lon float64) float64 {
	px := lon * pp.cosRef * (earthRadiusM * math.Pi / 180)
	py := lat * (earthRadiusM * math.Pi / 180)
	best := math.Inf(1)
	for i := 1; i < len(pp.xs); i++ {
		ax, ay := pp.xs[i-1], pp.ys[i-1]
		dx, dy := pp.xs[i]-ax, pp.ys[i]-ay
		l2 := dx*dx + dy*dy
		t := 0.0
		if l2 > 0 {
			t = math.Max(0, math.Min(1, ((px-ax)*dx+(py-ay)*dy)/l2))
		}
		best = math.Min(best, math.Hypot(px-(ax+t*dx), py-(ay+t*dy)))
	}
	return best
}

// near must agree with the plain distance for any point and limit: it is a speed-up, not an approximation.
func TestPathIndexNearMatchesTheDistance(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	path := make([][2]float64, 700)
	for i := range path {
		f := float64(i) / float64(len(path)-1)
		path[i] = [2]float64{33 + 5*f + 0.4*math.Sin(f*30), -100 + 9*f}
	}
	pp := projectPath(path)
	ix := newPathIndex(pp)
	disagree := 0
	for i := 0; i < 5000; i++ {
		lat, lon := 31+rng.Float64()*9, -102+rng.Float64()*13
		limit := []float64{0, 500, 5_000, 32_000, 80_000}[rng.Intn(5)]
		if got, want := ix.near(pp, lat, lon, limit), segDist(pp, lat, lon) <= limit; got != want {
			disagree++
		}
	}
	if disagree != 0 {
		t.Errorf("near disagreed with the distance %d times in 5000", disagree)
	}
	// A path of one chunk and one with a repeated point still work.
	small := projectPath([][2]float64{{34, -118}, {34, -118}, {34.1, -118}})
	if !newPathIndex(small).near(small, 34.05, -118, 10) {
		t.Error("a point on a short path is not near it")
	}
}

func TestTileCenterRoundTrips(t *testing.T) {
	for _, c := range [][3]float64{{34.05, -118.24}, {47.6, -122.3}, {-33.9, 151.2}, {64.1, -21.9}} {
		x, y := lonToTileX(c[1], chargersZoom), latToTileY(c[0], chargersZoom)
		lat, lon := tileCenter(x, y, chargersZoom)
		if lonToTileX(lon, chargersZoom) != x || latToTileY(lat, chargersZoom) != y {
			t.Errorf("(%v,%v): the centre of tile %d,%d is outside it", c[0], c[1], x, y)
		}
		if d := haversineM(c[0], c[1], lat, lon); d > 3500 {
			t.Errorf("(%v,%v) is %.0f m from its tile's centre", c[0], c[1], d)
		}
	}
}

// Visiting only the tiles near the route gives exactly the stations the walk of the whole box gives.
func TestQueryCorridorTileListMatchesTheBoxWalk(t *testing.T) {
	// Stations on tiles scattered over a region, some on the route, some off it, some in the box but far from it.
	rng := rand.New(rand.NewSource(11))
	var props []map[string]any
	var centres [][2]float64
	for i := 0; i < 60; i++ {
		props = append(props, map[string]any{"name": "S", "@id": "nrel/" + string(rune('a'+i%26)) + string(rune('A'+i/26)), "dcfc_count": 1})
	}
	file := archiveWithProps(t, props)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(file) }))
	defer srv.Close()
	a := New(srv.URL)
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(a.tiles) != len(props) {
		t.Fatalf("%d tiles listed, want %d", len(a.tiles), len(props))
	}
	for _, tl := range a.tiles {
		lat, lon := tileCenter(tl.x, tl.y, chargersZoom)
		centres = append(centres, [2]float64{lat, lon})
	}
	// A path through the middle of them, in several shapes and corridor widths.
	for i := 0; i < 12; i++ {
		var path [][2]float64
		a0, a1 := centres[rng.Intn(len(centres))], centres[rng.Intn(len(centres))]
		for k := 0; k <= 50; k++ {
			f := float64(k) / 50
			path = append(path, [2]float64{a0[0] + (a1[0]-a0[0])*f + 0.02*math.Sin(f*9), a0[1] + (a1[1]-a0[1])*f})
		}
		for _, ckm := range []float64{0, 1, 3, 10} {
			fast := a.tiles
			got, err1 := a.QueryCorridor(path, FilterDCFC, QueryCorridorOptions{CorridorKm: ckm})
			a.tiles = nil
			want, err2 := a.QueryCorridor(path, FilterDCFC, QueryCorridorOptions{CorridorKm: ckm})
			a.tiles = fast
			if err1 != nil || err2 != nil || !reflect.DeepEqual(got, want) {
				t.Errorf("path %d corridor %v km: %d stations with the tile list, %d walking the box (%v, %v)", i, ckm, len(got), len(want), err1, err2)
			}
		}
	}
}
