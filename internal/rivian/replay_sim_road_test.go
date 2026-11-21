package rivian

import (
	"fmt"
	"math"
	"testing"
)

// simStraightKm is the longest stretch between two path points a simulated drive may have without lying on a stored road. A stored shape is
// simplified to 20 m, so a straight road shows as a long segment (I-210 and I-15 reach 2 km); a line across the map does not lie on one.
const simStraightKm = 1.0

// simStraightFallback is the places the routine parks the truck that have no stored road out of them, so a short drive from there draws
// awayErrand's straight out-and-back. None today: every place has one.
var simStraightFallback = map[samplePoint]bool{}

// nearStoredRoadM is the distance from p to the nearest segment of any stored road shape.
func nearStoredRoadM(p samplePoint) float64 {
	best := math.MaxFloat64
	for _, enc := range sampleRouteShapes {
		pts := decodeSamplePolyline(enc)
		for i := 1; i < len(pts); i++ {
			if d := roadSegDistM(p, pts[i-1], pts[i]); d < best {
				best = d
			}
		}
	}
	return best
}

// roadSegDistM is the distance from p to segment ab, on a local flat projection.
func roadSegDistM(p, a, b samplePoint) float64 {
	k := math.Cos(a.Lat * math.Pi / 180)
	px, py := (p.Lon-a.Lon)*111320*k, (p.Lat-a.Lat)*111320
	bx, by := (b.Lon-a.Lon)*111320*k, (b.Lat-a.Lat)*111320
	l2 := bx*bx + by*by
	if l2 == 0 {
		return math.Hypot(px, py)
	}
	u := math.Max(0, math.Min(1, (px*bx+py*by)/l2))
	return math.Hypot(px-u*bx, py-u*by)
}

// Every simulated drive follows a road, from home (short and full) and from each place the routine parks the truck (short): a stretch over
// simStraightKm must lie on a stored road shape, except from the starts named in simStraightFallback.
func TestSimDrivesFollowRoads(t *testing.T) {
	for _, l := range simLegs() {
		if _, ok := sampleRouteShapes[routeKey(l[0], l[1])]; !ok {
			t.Errorf("sim leg %s has no road shape; regenerate sample_routes_gen.go", routeKey(l[0], l[1]))
		}
	}
	c, _ := sampleShared()
	places := map[samplePoint]bool{}
	for _, s := range c.segs {
		if s.kind != segDrive {
			places[s.path[0]] = true
		}
	}
	if !places[SampleHome] {
		t.Fatal("the routine never parks the truck at home")
	}
	for place := range places {
		name := fmt.Sprintf("%.4f,%.4f", place.Lat, place.Lon)
		atHome := haversineMeters(place.Lat, place.Lon, SampleHome.Lat, SampleHome.Lon) <= 300
		starts := startsIn(func(s sampleSeg) bool { return s.kind != segDrive && s.path[0] == place })
		kinds := []SimKind{SimDriveShort}
		if atHome {
			kinds = append(kinds, SimDrive)
		}
		_, _, hasRoad := awayRoad(place)
		if !atHome && !hasRoad && !simStraightFallback[place] {
			t.Errorf("%s: no stored road leaves here; add a leg or name it in simStraightFallback", name)
		}
		for _, kind := range kinds {
			var plan simPlan
			found := false
			for _, at := range starts {
				if p, err := planSim(kind, at); err == nil {
					plan, found = p, true
					break
				}
			}
			if !found {
				t.Errorf("%s %s: no start books", name, kind)
				continue
			}
			if simStraightFallback[place] {
				continue
			}
			path := segOf(plan, segDrive).path
			for i := 1; i < len(path); i++ {
				a, b := path[i-1], path[i]
				if haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon) <= simStraightKm*1000 {
					continue
				}
				mid := samplePoint{(a.Lat + b.Lat) / 2, (a.Lon + b.Lon) / 2}
				if d := nearStoredRoadM(mid); d > 30 {
					t.Errorf("%s %s: a %.1f km stretch whose middle is %.0f m from any stored road", name, kind, haversineMeters(a.Lat, a.Lon, b.Lat, b.Lon)/1000, d)
				}
			}
		}
	}
}

// The road an away drive takes is a prefix of a stored shape: it starts at the place, runs the promised distance, and the drive comes back the
// same way.
func TestAwayRoadIsAStoredPrefix(t *testing.T) {
	out, turn, ok := awayRoad(sampleOffice)
	if !ok {
		t.Fatal("no stored road leaves the office")
	}
	if out[0] != sampleOffice || out[len(out)-1] != turn {
		t.Errorf("road runs %v to %v, want %v to %v", out[0], out[len(out)-1], sampleOffice, turn)
	}
	if mi := pathMiles(out); math.Abs(mi-simAwayLegMiles) > 0.01 {
		t.Errorf("road is %.3f mi, want %.1f", mi, simAwayLegMiles)
	}
	for _, p := range out[1:] { // the place itself sits off the road by where the road snaps to it
		if d := nearStoredRoadM(p); d > 1 {
			t.Errorf("%v is %.1f m from every stored road", p, d)
		}
	}
	if _, _, ok := awayRoad(samplePoint{0, 0}); ok {
		t.Error("a place with no stored road got one")
	}
}
