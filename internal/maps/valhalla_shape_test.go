package maps

import (
	"context"
	"encoding/json"
	"errors"
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

// lastShapeFormat is the shape_format of the last request shapeServer saw.
var lastShapeFormat string

func shapeServer(t *testing.T, shape string) *Valhalla {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		lastShapeFormat, _ = req["shape_format"].(string)
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
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {10.0025, 10.001}, shapeTo}, 1e6))
	got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo)
	if err != nil || len(got) != 3 {
		t.Fatalf("shape = %v, %v; want three vertices", got, err)
	}
	// Decoded as polyline6, to the right coordinates.
	if d := haversineM(got[1][0], got[1][1], 10.0025, 10.001); d > 1 {
		t.Errorf("the middle vertex is %.1f m from where it was encoded", d)
	}
	// It asks for the format it decodes: polyline6, which is also what the in-cluster Valhalla sends whatever polyline5 is asked for.
	if lastShapeFormat != "polyline6" {
		t.Errorf("shape_format = %q, want polyline6", lastShapeFormat)
	}
}

// The case behind the owner's corrupt stored route, mirrored: the answer and the decoder disagree on the precision. The in-cluster Valhalla
// answered polyline6 to a request for polyline5 and the shape was read as polyline5, so the vertex landed ten times too far, off the globe,
// and a drive's stored path got two segments of thousands of kilometres out and back. Now the request and the decoder both say polyline6;
// and if they ever disagree again, in either direction, the shape is refused (a polyline5 answer read as polyline6 is ten times too
// small: nowhere near the ends).
func TestRouteShapeRefusesAShapeAtTheWrongPrecision(t *testing.T) {
	for name, scale := range map[string]float64{"polyline5 answer read as polyline6": 1e5} {
		v := shapeServer(t, encodeN([][2]float64{shapeFrom, {10.0025, 10.001}, shapeTo}, scale))
		got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo)
		if err == nil {
			t.Fatalf("%s: a shape at the wrong precision was accepted: %v", name, got)
		}
		// Typed, so the recorder can count it apart from a routing engine that is merely down.
		var bad interface {
			BadShape() bool
			ShapeKind() string
		}
		if !errors.As(err, &bad) || bad.ShapeKind() != "detour" {
			t.Errorf("%s: error %v is not a BadShapeError of kind detour", name, err)
		}
	}
}

func TestRouteShapeRefusesAVertexOffTheGlobe(t *testing.T) {
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {95, 200}, shapeTo}, 1e6)) // far outside +-90 / +-180
	got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo)
	if err == nil {
		t.Fatalf("a vertex off the globe was accepted: %v", got)
	}
	var bad interface{ ShapeKind() string }
	if !errors.As(err, &bad) || bad.ShapeKind() != "offglobe" {
		t.Errorf("error %v is not a BadShapeError of kind offglobe", err)
	}
}

func TestRouteShapeRefusesAVertexInAnotherCity(t *testing.T) {
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {20, 20}, shapeTo}, 1e6)) // a valid place, a thousand kilometres away
	if got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo); err == nil {
		t.Fatalf("a vertex far from both ends was accepted: %v", got)
	}
}

func TestRouteShapeAllowsAWindingRoad(t *testing.T) {
	// The road leaves the straight line by about twice its length (a detour round a block or a river): still a road.
	v := shapeServer(t, encodeN([][2]float64{shapeFrom, {10.006, 9.998}, {10.008, 10.006}, shapeTo}, 1e6))
	if got, err := v.RouteShape(context.Background(), shapeFrom, shapeTo); err != nil || len(got) != 4 {
		t.Fatalf("a winding road was refused: %v, %v", got, err)
	}
}
