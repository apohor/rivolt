package chargers

import (
	"context"
	"errors"
	"math"
	"testing"
)

// No test here makes an oversized request to anything: they exercise the
// validation that runs before any tile is read.

func TestPlanCorridorClampsAndCaps(t *testing.T) {
	_, box, err := planCorridor([][2]float64{{90, 0}, {-90, 0}}, 5)
	// Latitude beyond the map is clamped, so this spans the Mercator height at one
	// longitude column: 16384 tiles tall is fine.
	if err != nil {
		t.Fatalf("pole to pole: %v", err)
	}
	if box.tyMin != 0 || box.tyMax >= 1<<chargersZoom {
		t.Errorf("tile rows %d..%d out of range", box.tyMin, box.tyMax)
	}
	// A huge corridor is capped at 50 km.
	_, box, err = planCorridor([][2]float64{{34, -118}, {34.1, -118.1}}, 1e9)
	if err != nil || box.corridorKM != MaxCorridorKm {
		t.Errorf("corridor %v (%v), want %v", box.corridorKM, err, MaxCorridorKm)
	}
	// Zero or negative means the default.
	if _, box, _ := planCorridor([][2]float64{{34, -118}, {34.1, -118.1}}, -3); box.corridorKM != defaultCorridorKM {
		t.Errorf("default corridor: %v", box.corridorKM)
	}
	// Coordinates are clamped, and the input is not changed.
	in := [][2]float64{{89, 179.5}, {88, 500}}
	out, _, err := planCorridor(in, 5)
	if err != nil || out[0] != [2]float64{maxLatitude, 179.5} || out[1] != [2]float64{maxLatitude, 180} {
		t.Errorf("clamped %v (%v)", out, err)
	}
	if in[0] != [2]float64{89, 179.5} || in[1][1] != 500 {
		t.Error("the caller's path was modified")
	}
}

func TestPlanCorridorRefusesBadAndHugeQueries(t *testing.T) {
	for name, path := range map[string][][2]float64{
		"NaN latitude":  {{math.NaN(), 0}, {1, 1}},
		"Inf longitude": {{0, math.Inf(1)}, {1, 1}},
	} {
		if _, _, err := planCorridor(path, 5); !errors.Is(err, ErrBadPath) {
			t.Errorf("%s: %v, want ErrBadPath", name, err)
		}
	}
	long := make([][2]float64, MaxPathPoints+1)
	for i := range long {
		long[i] = [2]float64{34, -118}
	}
	if _, _, err := planCorridor(long, 5); !errors.Is(err, ErrQueryTooLarge) {
		t.Errorf("%d points: %v, want ErrQueryTooLarge", len(long), err)
	}
	if _, _, err := planCorridor(long[:MaxPathPoints], 5); err != nil {
		t.Errorf("%d points: %v", MaxPathPoints, err)
	}
	// Two opposite corners of the world: a box of about 268 million tiles.
	if _, _, err := planCorridor([][2]float64{{-80, -179}, {80, 179}}, 5); !errors.Is(err, ErrQueryTooLarge) {
		t.Errorf("world-spanning box: %v, want ErrQueryTooLarge", err)
	}
	// A coast-to-coast route is still fine.
	if _, _, err := planCorridor([][2]float64{{34.05, -118.24}, {40.71, -74.0}}, 5); err != nil {
		t.Errorf("Los Angeles to New York: %v", err)
	}
}

// A query whose context is already done returns that error before any tile is
// read (the archive is not even touched).
func TestQueryCorridorStopsOnACancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var a *Archive
	_, err := a.QueryCorridor([][2]float64{{34.05, -118.24}, {36.17, -115.14}}, FilterDCFC, QueryCorridorOptions{Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
