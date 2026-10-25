package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
	"github.com/apohor/rivolt/internal/maps"
)

// bendRouter is a router whose "road" between two points bends 0.3 degrees east of the straight line at its middle, so a test can
// tell a routed stretch from a chord. It counts its calls and the locations each was given.
type bendRouter struct {
	off   bool
	err   error
	calls [][][2]float64
}

func (b *bendRouter) Enabled() bool { return !b.off }

func (b *bendRouter) Route(_ context.Context, locs [][2]float64, _ maps.RouteOpts) (maps.Route, error) {
	b.calls = append(b.calls, locs)
	if b.err != nil {
		return maps.Route{}, b.err
	}
	var r maps.Route
	for i := 1; i < len(locs); i++ {
		a, c := locs[i-1], locs[i]
		mid := [2]float64{(a[0] + c[0]) / 2, (a[1]+c[1])/2 + 0.3}
		r.Legs = append(r.Legs, maps.RouteLeg{Shape: [][2]float64{a, {(a[0] + mid[0]) / 2, (a[1] + mid[1]) / 2}, mid, {(mid[0] + c[0]) / 2, (mid[1] + c[1]) / 2}, c}})
	}
	return r, nil
}

func TestDensifyLeavesAShortChordedPathAlone(t *testing.T) {
	r := &bendRouter{}
	p := straight(200) // 51 points, 4 km apart
	got, changed := densifyPath(context.Background(), r, p)
	if changed || len(got) != len(p) || len(r.calls) != 0 {
		t.Errorf("changed %v, %d points (want %d), %d router calls (want 0)", changed, len(got), len(p), len(r.calls))
	}
}

func TestDensifyRoutesTheLongChordsAndKeepsTheEnds(t *testing.T) {
	r := &bendRouter{}
	p := [][2]float64{{34, -118}, {36, -118}, {38, -118}} // two chords of about 220 km
	got, changed := densifyPath(context.Background(), r, p)
	if !changed || len(r.calls) != 1 || len(r.calls[0]) != 3 {
		t.Fatalf("changed %v, calls %d: want one call with the three points", changed, len(r.calls))
	}
	if got[0] != p[0] || got[len(got)-1] != p[2] {
		t.Errorf("ends %v .. %v, want %v .. %v", got[0], got[len(got)-1], p[0], p[2])
	}
	if len(got) != 9 { // two legs of five points, sharing the middle one
		t.Errorf("%d points, want 9", len(got))
	}
	bent := false
	for _, q := range got {
		bent = bent || q[1] > -117.8
	}
	if !bent {
		t.Error("the path still runs straight: the routed shape was not used")
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Errorf("point %d repeats the one before it", i)
		}
	}
}

func TestDensifyKeepsTheDenseStretchesAsTheyWere(t *testing.T) {
	r := &bendRouter{}
	// 10 points 4 km apart, one 220 km chord, 10 points 4 km apart.
	var p [][2]float64
	for i := 0; i < 10; i++ {
		p = append(p, [2]float64{34 + float64(i)*0.036, -118})
	}
	p = append(p, [2]float64{36.5, -118})
	for i := 1; i < 10; i++ {
		p = append(p, [2]float64{36.5 + float64(i)*0.036, -118})
	}
	got, changed := densifyPath(context.Background(), r, p)
	if !changed || len(r.calls) != 1 || len(r.calls[0]) != 2 {
		t.Fatalf("changed %v, calls %v: want one two-point call for the one chord", changed, r.calls)
	}
	for i := 0; i < 9; i++ {
		if got[i] != p[i] {
			t.Errorf("dense point %d changed", i)
		}
	}
	if got[len(got)-1] != p[len(p)-1] {
		t.Error("the last point changed")
	}
}

func TestDensifyIsBestEffort(t *testing.T) {
	p := [][2]float64{{34, -118}, {36, -118}}
	for name, r := range map[string]pathRouter{
		"no route":       &bendRouter{err: maps.ErrNoRoute},
		"upstream error": &bendRouter{err: errors.New("valhalla: HTTP 500")},
		"not configured": &bendRouter{off: true},
		"nil router":     nil,
	} {
		got, changed := densifyPath(context.Background(), r, p)
		if changed || len(got) != 2 {
			t.Errorf("%s: changed %v, %d points; want the path as it came", name, changed, len(got))
		}
	}
	// A coordinate that is not on the globe never reaches Valhalla.
	r := &bendRouter{}
	if _, changed := densifyPath(context.Background(), r, [][2]float64{{34, -118}, {95, -118}}); changed || len(r.calls) != 0 {
		t.Errorf("an invalid coordinate: changed %v, calls %d", changed, len(r.calls))
	}
}

func TestDensifyCapsTheRouterCallsOfOneRequest(t *testing.T) {
	r := &bendRouter{}
	// Alternate long chords with short ones so every long chord is its own run: more runs than the cap.
	var p [][2]float64
	lat := 20.0
	for i := 0; i < 20; i++ {
		p = append(p, [2]float64{lat, -100}, [2]float64{lat + 0.01, -100})
		lat += 1.0
	}
	densifyPath(context.Background(), r, p)
	if len(r.calls) != densifyMaxRuns {
		t.Errorf("%d router calls, want the cap of %d", len(r.calls), densifyMaxRuns)
	}
}

// Through the handler: a sparse path is put on the road before the search, and the answer says so; with no router it is searched
// as it came and the answer does not claim a snap.
func TestOvernightHandlerSnapsASparsePath(t *testing.T) {
	noLoad := func(context.Context) error { return nil }
	arch := hotelArchive{}
	sparse := [][2]float64{{34, -118}, {37, -118}, {40, -118}}
	body := map[string]any{"path": sparse, "drive_sec": 36000, "budget_hours": 8}

	var out struct {
		PathSnapped bool    `json:"path_snapped"`
		RouteM      float64 `json:"route_m"`
	}
	rec := postOvernight(overnightHandlerWith(arch, &bendRouter{}, nil, noLoad), body)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || !out.PathSnapped {
		t.Fatalf("%d %s, want path_snapped true", rec.Code, rec.Body.String())
	}
	snappedLen := out.RouteM

	out.PathSnapped = false
	rec = postOvernight(overnightHandlerWith(arch, nil, nil, noLoad), body)
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.PathSnapped {
		t.Fatalf("%d %s, want no path_snapped without a router", rec.Code, rec.Body.String())
	}
	if snappedLen <= out.RouteM {
		t.Errorf("the snapped route (%v m) is not longer than the chords (%v m)", snappedLen, out.RouteM)
	}
}

var _ = chargers.FilterHotels
