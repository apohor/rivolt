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
