package chargers

import (
	"math"
	"testing"
)

// The reason PointQueryPath exists: a station is kept only when its nearest point on the path is not an end of it. Checked with the
// archive's own projection (the same call featuresInTile makes), around a point in every direction.
func TestPointQueryPathKeepsEverythingWithinTheRadius(t *testing.T) {
	const lat, lon, radiusKm = 36.90, -104.44, 30.0
	accept := func(path [][2]float64, pLat, pLon float64) bool {
		p, _, err := planCorridor(path, radiusKm)
		if err != nil {
			t.Fatalf("planCorridor: %v", err)
		}
		proj := projectPath(p)
		d := proj.perpDistanceM(pLat, pLon, 1, false) // IncludeEndpoints: the trim is off
		return !math.IsInf(d, 1) && d <= radiusKm*1000
	}
	at := func(bearingDeg, km float64) (float64, float64) {
		b := bearingDeg * math.Pi / 180
		return lat + km*math.Cos(b)/111.195, lon + km*math.Sin(b)/(111.195*math.Cos(lat*math.Pi/180))
	}
	good := PointQueryPath(lat, lon, radiusKm)
	tiny := [][2]float64{{lat, lon}, {lat + 0.0001, lon}}
	for bearing := 0.0; bearing < 360; bearing += 15 {
		for _, km := range []float64{0.5, 8, 20, 29} {
			pLat, pLon := at(bearing, km)
			if !accept(good, pLat, pLon) {
				t.Errorf("a station %.0f km away at %.0f deg is dropped by PointQueryPath", km, bearing)
			}
		}
	}
	// The pitfall itself: the tiny path drops a station 8 km away to the north-east, which is why callers must not use one.
	pLat, pLon := at(45, 8)
	if accept(tiny, pLat, pLon) {
		t.Error("the tiny path now keeps a station 8 km to its north-east: PointQueryPath may no longer be needed")
	}
	// Stations beyond the radius stay out of the band, not by luck of the projection.
	pLat, pLon = at(90, 45)
	if accept(good, pLat, pLon) {
		t.Error("a station 45 km away is kept for a 30 km radius")
	}
}

func TestPointQueryPathStaysOnTheGlobe(t *testing.T) {
	for _, lat := range []float64{-90, -89.95, 89.95, 90, 0} {
		p := PointQueryPath(lat, 10, 30)
		if len(p) != 2 || p[0][0] < -90 || p[1][0] > 90 || p[0][0] >= p[1][0] {
			t.Errorf("lat %v: %v", lat, p)
		}
	}
}
