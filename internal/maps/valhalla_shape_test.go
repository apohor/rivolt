package maps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A route-fill shape is spliced into a drive's stored path, so RouteShape refuses one that cannot be a road between the two ends: a vertex off
// the globe (a polyline6 string read as polyline5 is ten times too large) or nowhere near the ends. Plainly fake places near (10, 10).

// encodeN encodes (lat, lon) pairs as a polyline whose coordinates are scaled by scale (1e5 or 1e6): the test's own encoder.
func encodeN(pts [][2]float64, scale float64) string {
	var sb strings.Builder
	var pl, pn int
	put := func(v int) {
		v <<= 1
		if v < 0 {
			v = ^v
		}
		for v >= 0x20 {
			sb.WriteByte(byte((0x20 | (v & 0x1f)) + 63))
			v >>= 5
		}
		sb.WriteByte(byte(v + 63))
	}
	for _, p := range pts {
		la, lo := int(p[0]*scale+0.5*sign(p[0])), int(p[1]*scale+0.5*sign(p[1]))
		put(la - pl)
		put(lo - pn)
		pl, pn = la, lo
	}
	return sb.String()
}

func shapeServer(t *testing.T, shape string) *Valhalla {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"trip": map[string]any{"legs": []map[string]any{{"shape": shape}}}})
	}))
	t.Cleanup(srv.Close)
	return &Valhalla{HTTP: srv.Client(), BaseURL: srv.URL}
}

var (
	shapeFrom = [2]float64{10.000, 10.000}
	shapeTo   = [2]float64{10.003, 10.003} // about 470 m
)

func TestRouteShapeAcceptsARoadBetweenTheEnds(t *testing.T) {
	// Three vertices: the ends and one in the middle, which a winding road may leave the straight line for.
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {10.0025, 10.001}, shapeTo}, 1e5))
	got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo)
	if err != nil || len(got) != 3 {
		t.Fatalf("shape = %v, %v; want three vertices", got, err)
	}
}

// The case behind the owner's corrupt stored route: the answer is polyline6 and is read as polyline5, so the one middle vertex lands ten
// times too far, off the globe, and the drive's stored path gets two segments of thousands of kilometres out and back.
func TestRouteShapeRefusesAShapeAtTheWrongPrecision(t *testing.T) {
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {10.0025, 10.001}, shapeTo}, 1e6))
	got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo)
	if err == nil {
		t.Fatalf("a shape ten times too large was accepted: %v", got)
	}
}

func TestRouteShapeRefusesAVertexOffTheGlobe(t *testing.T) {
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {95, 200}, shapeTo}, 1e5)) // far outside +-90 / +-180
	if got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo); err == nil {
		t.Fatalf("a vertex off the globe was accepted: %v", got)
	}
}

func TestRouteShapeRefusesAVertexInAnotherCity(t *testing.T) {
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {20, 20}, shapeTo}, 1e5)) // a valid place, a thousand kilometres away
	if got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo); err == nil {
		t.Fatalf("a vertex far from both ends was accepted: %v", got)
	}
}

func TestRouteShapeAllowsAWindingRoad(t *testing.T) {
	// The road leaves the straight line by about twice its length (a detour round a block or a river): still a road.
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {10.006, 9.998}, {10.008, 10.006}, shapeTo}, 1e5))
	if got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo); err != nil || len(got) != 4 {
		t.Fatalf("a winding road was refused: %v, %v", got, err)
	}
}
