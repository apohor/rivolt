package routepoints

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/repair/routegaps"
	"github.com/apohor/rivolt/internal/rivian"
)

// encode is the test's own precision-5 encoder. rivian.EncodePolyline refuses a point off the globe now, so a corrupt stored route can only
// be built by hand. Plainly fake places near (10, 10).
func encode(pts [][2]float64) string {
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
		la, lo := int(math.Round(p[0]*1e5)), int(math.Round(p[1]*1e5))
		put(la - pl)
		put(lo - pn)
		pl, pn = la, lo
	}
	return sb.String()
}

var (
	good    = [][2]float64{{10.000, 10.000}, {10.001, 10.001}, {10.002, 10.002}, {10.003, 10.003}, {10.004, 10.004}}
	bad     = [2]float64{100.25, -970.1} // off the globe on both axes
	withBad = [][2]float64{good[0], good[1], bad, good[2], good[3], good[4]}
)

func TestStripDropsPointsOffTheGlobeAndKeepsTheRest(t *testing.T) {
	kept, n := Strip(withBad)
	if n != 1 || len(kept) != len(good) {
		t.Fatalf("kept %d, removed %d; want %d and 1", len(kept), n, len(good))
	}
	for i := range good {
		if kept[i] != good[i] {
			t.Errorf("point %d = %v, want %v", i, kept[i], good[i])
		}
	}
	if _, n := Strip([][2]float64{{math.NaN(), 0}, {0, math.Inf(1)}, {91, 0}, {0, -181}, {90, 180}, {-90, -180}}); n != 4 {
		t.Errorf("removed %d of the edge cases, want the 4 off the globe (the corners are on it)", n)
	}
}

// The repair writes back what the old route was, minus the bad point: no other point moves, so a good route's re-encoding is identical.
func TestRepairedRouteIsTheRouteWithoutTheBadPoint(t *testing.T) {
	pts, err := routegaps.Decode(encode(withBad))
	if err != nil {
		t.Fatal(err)
	}
	kept, _ := Strip(pts)
	if got, want := rivian.EncodePolyline(kept), encode(good); got != want {
		t.Fatalf("repaired = %q, want the route without the point (%q)", got, want)
	}
}

func TestReEncodingAGoodRouteChangesNothing(t *testing.T) {
	route := encode(good)
	pts, err := routegaps.Decode(route)
	if err != nil {
		t.Fatal(err)
	}
	if got := rivian.EncodePolyline(pts); got != route {
		t.Fatalf("re-encoded = %q, want it unchanged (%q)", got, route)
	}
}

func cand(route string, month time.Month) Candidate {
	return Candidate{ID: uuid.New(), UserID: uuid.New(), StartedAt: time.Date(2026, month, 5, 12, 0, 0, 0, time.UTC), Polyline: route}
}

func TestMakePlanCountsWithoutLeakingAnything(t *testing.T) {
	cands := []Candidate{
		cand(encode(good), time.October),
		cand(encode(withBad), time.October),
		cand(encode(withBad), time.November),
		cand("not a polyline \x01", time.November),
		cand(encode([][2]float64{bad, {95, 0}, good[0]}), time.November), // one good point left: nothing to draw
	}
	p := MakePlan(cands)
	if p.Scanned != 5 || p.Undecoded != 1 || p.BadPoints != 4 || p.Unfixable != 1 || len(p.Drives) != 2 {
		t.Fatalf("plan = scanned %d, undecoded %d, bad %d, unfixable %d, drives %d; want 5, 1, 4, 1, 2", p.Scanned, p.Undecoded, p.BadPoints, p.Unfixable, len(p.Drives))
	}
	if p.Rescaled != 0 || p.Dropped != 2 {
		t.Errorf("rescaled %d, dropped %d in the repairable routes; want 0 and 2", p.Rescaled, p.Dropped)
	}
	if m := p.ByMonth(); m["2026-10"] != 1 || m["2026-11"] != 1 || len(m) != 2 {
		t.Errorf("by month = %v, want one in each of October and November", m)
	}
	ids := p.IDs()
	if len(ids) != 2 || ids[0].String() >= ids[1].String() {
		t.Errorf("ids = %v, want two, sorted", ids)
	}
	if d := Digest(ids); len(d) != 64 || d != Digest([]uuid.UUID{ids[1], ids[0]}) {
		t.Errorf("digest %q is not a stable sha256 of the sorted list", d)
	}
}

func TestMakePlanOfNothing(t *testing.T) {
	p := MakePlan(nil)
	if p.Scanned != 0 || len(p.Drives) != 0 || len(p.IDs()) != 0 {
		t.Fatalf("plan = %+v, want empty", p)
	}
}

// The cause: a precision-6 routing vertex stored as precision 5, which is ten times too large. Put back by a tenth it lands on the road
// between its neighbours, so the fill it belonged to survives.
func TestRepairPutsATenfoldVertexBackAndDropsAnyOtherBadPoint(t *testing.T) {
	mid := [2]float64{10.0015, 10.0015} // a fill vertex between good[1] and good[2]
	tenfold := [2]float64{mid[0] * 10, mid[1] * 10}
	out, rescaled, dropped := Repair([][2]float64{good[0], good[1], tenfold, good[2], good[3]})
	if rescaled != 1 || dropped != 0 || len(out) != 5 {
		t.Fatalf("rescaled %d, dropped %d, %d points; want 1, 0, 5", rescaled, dropped, len(out))
	}
	if math.Abs(out[2][0]-mid[0]) > 1e-9 || math.Abs(out[2][1]-mid[1]) > 1e-9 {
		t.Errorf("the point came back as %v, want %v", out[2], mid)
	}
	for i, p := range [][2]float64{good[0], good[1], mid, good[2], good[3]} {
		if rescaled := out[i]; math.Abs(rescaled[0]-p[0]) > 1e-9 || math.Abs(rescaled[1]-p[1]) > 1e-9 {
			t.Errorf("point %d = %v, want %v", i, rescaled, p)
		}
	}
	// Re-encoded at precision 5 (the stored format), the repaired route is the route as it would have been recorded.
	if got, want := rivian.EncodePolyline(out), encode([][2]float64{good[0], good[1], mid, good[2], good[3]}); got != want {
		t.Errorf("repaired route %q, want %q", got, want)
	}
}

func TestRepairDropsABadPointThatDoesNotFallOnTheRoadWhenDividedByTen(t *testing.T) {
	// A tenth of this is a real place, but nowhere near the road between the neighbours.
	out, rescaled, dropped := Repair(withBad)
	if rescaled != 0 || dropped != 1 || len(out) != len(good) {
		t.Fatalf("rescaled %d, dropped %d, %d points; want 0, 1, %d", rescaled, dropped, len(out), len(good))
	}
}

func TestRepairDropsABadPointAtEitherEndAndNeverGuesses(t *testing.T) {
	first := [2]float64{good[0][0] * 10, good[0][1] * 10}
	last := [2]float64{good[4][0] * 10, good[4][1] * 10}
	out, rescaled, dropped := Repair([][2]float64{first, good[1], good[2], good[3], last})
	if rescaled != 0 || dropped != 2 || len(out) != 3 {
		t.Fatalf("rescaled %d, dropped %d, %d points; want 0, 2, 3 (no neighbour on one side, so no way to check)", rescaled, dropped, len(out))
	}
}

func TestRepairJudgesARunOfBadPointsAgainstTheRoadNotEachOther(t *testing.T) {
	m1, m2 := [2]float64{10.0012, 10.0012}, [2]float64{10.0018, 10.0018}
	out, rescaled, dropped := Repair([][2]float64{good[0], good[1], {m1[0] * 10, m1[1] * 10}, {m2[0] * 10, m2[1] * 10}, good[2], good[3]})
	if rescaled != 2 || dropped != 0 || len(out) != 6 {
		t.Fatalf("rescaled %d, dropped %d, %d points; want 2, 0, 6", rescaled, dropped, len(out))
	}
	if math.Abs(out[2][0]-m1[0]) > 1e-9 || math.Abs(out[3][0]-m2[0]) > 1e-9 {
		t.Errorf("the run came back as %v %v", out[2], out[3])
	}
}

func TestRepairLeavesAGoodRouteAlone(t *testing.T) {
	out, rescaled, dropped := Repair(good)
	if rescaled != 0 || dropped != 0 || len(out) != len(good) {
		t.Fatalf("rescaled %d, dropped %d; want none", rescaled, dropped)
	}
}
