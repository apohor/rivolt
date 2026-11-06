package maps

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The encoder reproduces Google's documented example at precision 5, and round-trips at precision 6 without loss.
func TestEncodePolylineMatchesTheKnownVectorAndRoundTrips(t *testing.T) {
	pts := [][2]float64{{38.5, -120.2}, {40.7, -120.95}, {43.252, -126.453}}
	if got := encodePolyline(pts, 1e5); got != "_p~iF~ps|U_ulLnnqC_mqNvxq`@" {
		t.Errorf("precision 5 = %q", got)
	}
	line := [][2]float64{{34.052235, -118.243683}, {34.052300, -118.243700}, {35.0, -117.5}, {-33.868820, 151.209296}}
	back := DecodePolyline6(EncodePolyline6(line))
	if len(back) != len(line) {
		t.Fatalf("%d points back, want %d", len(back), len(line))
	}
	for i := range line {
		if d := haversineM(line[i][0], line[i][1], back[i][0], back[i][1]); d > 0.2 {
			t.Errorf("point %d moved %.2f m", i, d)
		}
	}
	if EncodePolyline6(nil) != "" {
		t.Error("no points should encode to an empty string")
	}
}

func fakeValhalla(t *testing.T, legs ...string) *Valhalla {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type leg struct {
			Summary struct {
				Length float64 `json:"length"`
				Time   float64 `json:"time"`
			} `json:"summary"`
			Shape string `json:"shape"`
		}
		var out struct {
			Trip struct {
				Legs []leg `json:"legs"`
			} `json:"trip"`
		}
		for _, s := range legs {
			l := leg{Shape: s}
			l.Summary.Length, l.Summary.Time = 10, 600
			out.Trip.Legs = append(out.Trip.Legs, l)
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return NewValhalla(srv.URL)
}

// The guard runs on every leg: a leg whose shape lies in another city (polyline5 read as polyline6 is ten times too small) is refused, not drawn.
func TestRouteCheckedRefusesAShapeThatCannotBeTheRoad(t *testing.T) {
	a, b, c := [2]float64{34.05, -118.24}, [2]float64{34.5, -118.0}, [2]float64{35.0, -117.5}
	good1, good2 := EncodePolyline6([][2]float64{a, b}), EncodePolyline6([][2]float64{b, c})
	v := fakeValhalla(t, good1, good2)
	rt, err := v.RouteChecked(context.Background(), [][2]float64{a, b, c}, RouteOpts{})
	if err != nil || len(rt.Legs) != 2 {
		t.Fatalf("a good route: %v %v", rt, err)
	}
	// the second leg is the right shape written at precision 5: read as precision 6 it is ten times too small
	wrong := encodePolyline([][2]float64{b, c}, 1e5)
	v = fakeValhalla(t, good1, wrong)
	_, err = v.RouteChecked(context.Background(), [][2]float64{a, b, c}, RouteOpts{})
	var bad *BadShapeError
	if !errors.As(err, &bad) || bad.Kind != "detour" {
		t.Errorf("a leg decoded at the wrong precision: %v, want a detour refusal", err)
	}
	// plain Route (the planner's own use) is unchanged: it does not apply the guard
	if _, err := v.Route(context.Background(), [][2]float64{a, b, c}, RouteOpts{}); err != nil {
		t.Errorf("Route must stay as it was: %v", err)
	}
}
