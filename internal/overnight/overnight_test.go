package overnight

import (
	"errors"
	"math"
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
)

// A straight route up a meridian from 34N: 1 degree of latitude is 111.2 km.
func route(km float64) [][2]float64 {
	var p [][2]float64
	const steps = 100
	for i := 0; i <= steps; i++ {
		p = append(p, [2]float64{34 + km/111.195*float64(i)/steps, -118})
	}
	return p
}

// fakeArchive answers the hotel and the fast-charger queries from lists, as
// the real one would for the corridor asked about.
type fakeArchive struct {
	hotels []chargers.POI
	camps  []chargers.POI
	dcfc   []chargers.POI
	err    error
	calls  int
	last   [][2]float64
}

func (f *fakeArchive) QueryCorridor(path [][2]float64, filter chargers.Filter, _ chargers.QueryCorridorOptions) ([]chargers.POI, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if filter == chargers.FilterHotels {
		f.last = path
		return f.hotels, nil
	}
	if filter == chargers.FilterCamp {
		return f.camps, nil
	}
	return f.dcfc, nil
}

func hotel(name string, km, offKm, kw float64) chargers.POI {
	return chargers.POI{
		Name: name, EntityID: name, Lat: 34 + km/111.195, Lon: -118 + offKm/111.195/math.Cos(34*math.Pi/180),
		IsL2: true, MaxPowerKW: kw, FacilityType: "HOTEL", L2Count: 2,
	}
}

func TestSuggestsHotelsNearTheBudgetPoint(t *testing.T) {
	a := &fakeArchive{hotels: []chargers.POI{
		hotel("Near target, strong", 800, 0.5, 11.5),
		hotel("Near target, weak", 800, 0.5, 3.3),
		hotel("Far off route, strong", 810, 10, 11.5),
		hotel("Too early", 300, 0, 19.2), // outside the window, never suggested
		hotel("Unlisted power", 790, 1, 0),
	}}
	// 1000 km in 10 h; an 8 h budget reaches 800 km. The window is 30 min either way: 750-850 km.
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Target.AlongM-800_000) > 500 || res.Target.ReachesEnd || math.Abs(res.RouteM-1_000_000) > 1500 {
		t.Errorf("target %+v route %v", res.Target, res.RouteM)
	}
	if len(res.Candidates) != 3 {
		t.Fatalf("%d candidates, want 3: %+v", len(res.Candidates), res.Candidates)
	}
	c := res.Candidates
	if c[0].Name != "Near target, strong" || c[0].L2KW != 11.5 || c[0].L2KWAssumed {
		t.Errorf("first = %+v", c[0])
	}
	for _, x := range c {
		if x.Name == "Too early" {
			t.Error("a hotel outside the window must not be suggested")
		}
		if x.AlongM < 745_000 || x.AlongM > 855_000 {
			t.Errorf("%s is %v m along, outside the window", x.Name, x.AlongM)
		}
	}
	// The unlisted-power hotel is assumed 7 kW and says so.
	for _, x := range c {
		if x.Name == "Unlisted power" && (x.L2KW != 7 || !x.L2KWAssumed) {
			t.Errorf("unlisted power = %+v", x)
		}
	}
	// Detour is the round trip to the route: the hotel is 0.5 km west of the
	// route's longitude at 34N, which is ~0.45 km at 41N, so ~0.9 km there and back.
	if math.Abs(c[0].DetourM-906) > 40 {
		t.Errorf("detour = %v m, want ~906", c[0].DetourM)
	}
	// The hotel query is bounded to the window, not the whole route.
	if len(a.last) < 2 {
		t.Fatal("no hotel query")
	}
	first, last := a.last[0], a.last[len(a.last)-1]
	if first[0] < 34+745/111.195 || last[0] > 34+855/111.195 {
		t.Errorf("queried %v..%v, want about 750-850 km along", first, last)
	}
}

func TestRankingAndTheMorningFastCharger(t *testing.T) {
	a := &fakeArchive{
		hotels: []chargers.POI{
			hotel("Strong, far", 800, 8, 11.5),
			hotel("Weak, with a fast charger", 800, 0.3, 3.3),
			hotel("Weak, nothing near", 790, 0.3, 3.3),
		},
		dcfc: []chargers.POI{{Name: "Fast", Lat: 34 + 800/111.195, Lon: -118, IsDCFC: true, MaxPowerKW: 150}},
	}
	// Every hotel sees the same fast charger in this fake, so give two of them none.
	b := &perHotelArchive{fakeArchive: a, dcfcFor: map[string]bool{"Weak, with a fast charger": true}}
	res, err := Suggest(b, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8})
	if err != nil {
		t.Fatal(err)
	}
	var withFast, without *Candidate
	for i := range res.Candidates {
		switch res.Candidates[i].Name {
		case "Weak, with a fast charger":
			withFast = &res.Candidates[i]
		case "Weak, nothing near":
			without = &res.Candidates[i]
		}
	}
	if withFast == nil || without == nil || withFast.DCFCNearby == nil || without.DCFCNearby != nil {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
	if withFast.DCFCNearby.Name != "Fast" || withFast.DCFCNearby.MaxPowerKW != 150 {
		t.Errorf("dcfc = %+v", withFast.DCFCNearby)
	}
	// With a fast charger nearby the weak hotel outranks the same hotel without one.
	idx := func(n string) int {
		for i, c := range res.Candidates {
			if c.Name == n {
				return i
			}
		}
		return 99
	}
	if idx("Weak, with a fast charger") > idx("Weak, nothing near") {
		t.Errorf("order = %+v", res.Candidates)
	}
}

// perHotelArchive returns the fast charger only for queries centred on a hotel
// that is listed as having one.
type perHotelArchive struct {
	*fakeArchive
	dcfcFor map[string]bool
}

func (p *perHotelArchive) QueryCorridor(path [][2]float64, filter chargers.Filter, o chargers.QueryCorridorOptions) ([]chargers.POI, error) {
	if filter != chargers.FilterDCFC {
		return p.fakeArchive.QueryCorridor(path, filter, o)
	}
	for _, h := range p.hotels {
		if p.dcfcFor[h.Name] && math.Abs(h.Lat-path[0][0]) < 1e-6 && math.Abs(h.Lon-path[0][1]) < 1e-6 {
			return p.dcfc, nil
		}
	}
	return nil, nil
}

func TestBudgetInMilesAndDayThatFits(t *testing.T) {
	a := &fakeArchive{hotels: []chargers.POI{hotel("H", 480, 0, 7)}}
	// 300 miles = 482.8 km.
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetMiles: 300})
	if err != nil || math.Abs(res.Target.AlongM-482_803) > 800 {
		t.Fatalf("miles budget: %+v, %v", res.Target, err)
	}
	// A day that fits inside the budget: the target is the end, flagged.
	res, err = Suggest(a, Request{Path: route(200), DriveSec: 2 * 3600, BudgetHours: 8})
	if err != nil || !res.Target.ReachesEnd || math.Abs(res.Target.AlongM-200_000) > 300 {
		t.Errorf("a short day: %+v, %v", res.Target, err)
	}
	// No drive time given: 90 km/h is assumed, so 8 h is 720 km.
	res, _ = Suggest(a, Request{Path: route(1000)})
	if math.Abs(res.Target.AlongM-720_000) > 1500 {
		t.Errorf("default budget lands at %v", res.Target.AlongM)
	}
}

func TestNoHotelsIsAnEmptyListNotAnError(t *testing.T) {
	res, err := Suggest(&fakeArchive{}, Request{Path: route(500), BudgetHours: 4})
	if err != nil || res.Candidates == nil || len(res.Candidates) != 0 {
		t.Errorf("res %+v err %v", res, err)
	}
	// Non-Level-2 listings are ignored.
	res, _ = Suggest(&fakeArchive{hotels: []chargers.POI{{Name: "DC only", Lat: 34.5, Lon: -118, IsDCFC: true}}}, Request{Path: route(1000), BudgetHours: 4})
	if len(res.Candidates) != 0 {
		t.Error("a listing with no Level 2 must not be suggested")
	}
}

func TestDeterministicAndValidating(t *testing.T) {
	a := &fakeArchive{hotels: []chargers.POI{hotel("B", 800, 1, 7), hotel("A", 800, 1, 7)}} // an exact tie
	r1, _ := Suggest(a, Request{Path: route(1000), DriveSec: 36000, BudgetHours: 8})
	r2, _ := Suggest(a, Request{Path: route(1000), DriveSec: 36000, BudgetHours: 8})
	if len(r1.Candidates) != 2 || r1.Candidates[0].Name != "A" || r1.Candidates[0] != r2.Candidates[0] {
		t.Errorf("ties must rank by name, the same way each time: %+v", r1.Candidates)
	}
	for name, req := range map[string]Request{
		"one point":       {Path: [][2]float64{{34, -118}}},
		"no length":       {Path: [][2]float64{{34, -118}, {34, -118}}},
		"bad latitude":    {Path: [][2]float64{{95, -118}, {34, -118}}},
		"nan":             {Path: [][2]float64{{math.NaN(), -118}, {34, -118}}},
		"negative budget": {Path: route(100), BudgetHours: -1},
		"negative miles":  {Path: route(100), BudgetMiles: -1},
		"too many points": {Path: make([][2]float64, MaxPathPoints+1)},
	} {
		if _, err := Suggest(&fakeArchive{}, req); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// An archive failure surfaces.
	if _, err := Suggest(&fakeArchive{err: errors.New("archive down")}, Request{Path: route(500), BudgetHours: 4}); err == nil {
		t.Error("an archive error must surface")
	}
}

func campAt(name string, km, offKm float64, amps int) chargers.POI {
	return chargers.POI{
		Name: name, EntityID: "ridb/" + name, Lat: 34 + km/111.195, Lon: -118 + offKm/111.195/math.Cos(34*math.Pi/180),
		FacilityType: "CAMPGROUND", HookupAmps: amps, ElectricSites: 10, Sites: 40, RIDBID: "251906", Source: "ridb",
	}
}

func kinds(cs []Candidate) (hotels, camps int) {
	for _, c := range cs {
		if c.Kind == KindCampground {
			camps++
		} else {
			hotels++
		}
	}
	return
}

// With campgrounds asked for, one of the three goes to the best campground in
// reach even when hotels outscore it; the others stay the best hotels.
func TestOneSlotIsReservedForTheBestCampground(t *testing.T) {
	var hs []chargers.POI
	for _, n := range []string{"A", "B", "C", "D", "E"} {
		hs = append(hs, hotel("Hotel "+n, 800, 0.3, 11.5))
	}
	a := &fakeArchive{hotels: hs, camps: []chargers.POI{campAt("Weak camp", 800, 5, 30), campAt("Best camp", 800, 1, 50)}}
	req := Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8, Kinds: []string{KindHotel, KindCampground}}
	res, err := Suggest(a, req)
	if err != nil {
		t.Fatal(err)
	}
	if h, c := kinds(res.Candidates); len(res.Candidates) != 3 || h != 2 || c != 1 {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
	for _, c := range res.Candidates {
		if c.Kind == KindCampground && c.Name != "Best camp" {
			t.Errorf("the reserved slot went to %q", c.Name)
		}
	}
	// Hotels only, as before: three hotels.
	req.Kinds = nil
	res, _ = Suggest(a, req)
	if h, c := kinds(res.Candidates); h != 3 || c != 0 {
		t.Errorf("hotels only = %+v", res.Candidates)
	}
}

// The campground is kept even when more than ten hotels outrank it.
func TestReservedCampgroundSurvivesTheTopTenCut(t *testing.T) {
	var hs []chargers.POI
	for i := 0; i < 14; i++ {
		hs = append(hs, hotel(string(rune('A'+i)), 800, 0.3, 11.5))
	}
	a := &fakeArchive{hotels: hs, camps: []chargers.POI{campAt("Only camp", 800, 6, 50)}}
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8, Kinds: []string{KindHotel, KindCampground}})
	if err != nil {
		t.Fatal(err)
	}
	if h, c := kinds(res.Candidates); h != 2 || c != 1 {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
}

// No campground in reach (none listed, one outside the day's window, or one
// too slow to offer) leaves the three best hotels; nothing is padded.
func TestNoCampgroundInReachLeavesHotels(t *testing.T) {
	var hs []chargers.POI
	for _, n := range []string{"A", "B", "C", "D"} {
		hs = append(hs, hotel("Hotel "+n, 800, 0.3, 11.5))
	}
	for name, camps := range map[string][]chargers.POI{
		"none":       nil,
		"too early":  {campAt("Far back", 300, 0, 50)},
		"120 V slow": {campAt("Slow", 800, 0, 20)},
	} {
		res, err := Suggest(&fakeArchive{hotels: hs, camps: camps}, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8, Kinds: []string{KindHotel, KindCampground}})
		if err != nil {
			t.Fatal(err)
		}
		if h, c := kinds(res.Candidates); h != 3 || c != 0 {
			t.Errorf("%s: candidates = %+v", name, res.Candidates)
		}
	}
}

// Campgrounds only: the three best campgrounds, as before.
func TestCampgroundsOnlyIsUnchanged(t *testing.T) {
	a := &fakeArchive{camps: []chargers.POI{campAt("A", 800, 0, 50), campAt("B", 800, 1, 50), campAt("C", 800, 2, 50), campAt("D", 800, 3, 50)}}
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8, Kinds: []string{KindCampground}})
	if err != nil {
		t.Fatal(err)
	}
	if h, c := kinds(res.Candidates); h != 0 || c != 3 {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
}

// 1000 km in 10 h (100 km/h), an 8 h budget: the budget point is 800 km. The first window is 750-850 km; it widens
// to -60/+60 min (700-900 km), then -120/+60 (600-900), then -180/+60 (500-900). The after side never passes an hour.
func suggestHotels(t *testing.T, hotels ...chargers.POI) *Result {
	t.Helper()
	res, err := Suggest(&fakeArchive{hotels: hotels}, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestWidensOnlyWhenTheFirstWindowIsEmpty(t *testing.T) {
	res := suggestHotels(t, hotel("In the first window", 760, 0, 7), hotel("Earlier", 650, 0, 19.2))
	if len(res.Candidates) != 1 || res.Candidates[0].Name != "In the first window" {
		t.Fatalf("candidates %+v", res.Candidates)
	}
	if res.Target.WindowBeforeMin != 30 || res.Target.WindowAfterMin != 30 {
		t.Errorf("window %v/%v, want 30/30", res.Target.WindowBeforeMin, res.Target.WindowAfterMin)
	}
}

func TestWidensToTheFirstWindowThatHasAStay(t *testing.T) {
	cases := []struct {
		name          string
		km            float64
		before, after float64
	}{
		{"54 min after", 890, 60, 60},
		{"75 min before", 725, 60, 60},
		{"90 min before", 650, 120, 60},
		{"150 min before", 550, 180, 60},
	}
	for _, c := range cases {
		res := suggestHotels(t, hotel("Only stay", c.km, 0, 7))
		if len(res.Candidates) != 1 {
			t.Errorf("%s: %d candidates", c.name, len(res.Candidates))
			continue
		}
		if res.Target.WindowBeforeMin != c.before || res.Target.WindowAfterMin != c.after {
			t.Errorf("%s: window %v/%v, want %v/%v", c.name, res.Target.WindowBeforeMin, res.Target.WindowAfterMin, c.before, c.after)
		}
	}
}

// Stopping early is safe; suggesting a stay more than an hour past the budget point (an 11-hour day) is not.
func TestNeverSuggestsMoreThanAnHourPastTheBudgetPoint(t *testing.T) {
	res := suggestHotels(t, hotel("Ninety minutes late", 950, 0, 19.2), hotel("Just over an hour late", 910, 0, 19.2))
	if len(res.Candidates) != 0 {
		t.Fatalf("suggested %+v", res.Candidates)
	}
	if res.Target.WindowBeforeMin != 180 || res.Target.WindowAfterMin != 60 {
		t.Errorf("empty result reports window %v/%v, want the widest tried, 180/60", res.Target.WindowBeforeMin, res.Target.WindowAfterMin)
	}
	// With an earlier stay available, that one is offered instead.
	res = suggestHotels(t, hotel("Ninety minutes late", 950, 0, 19.2), hotel("Early", 520, 0, 3.3))
	if len(res.Candidates) != 1 || res.Candidates[0].Name != "Early" {
		t.Fatalf("candidates %+v", res.Candidates)
	}
}

// In a widened window the stay nearest the budget point comes first, an earlier one winning a tie, whatever the power.
func TestWidenedWindowRanksByDistanceFromTheBudgetPoint(t *testing.T) {
	res := suggestHotels(t,
		hotel("Far, strong", 640, 0, 19.2),
		hotel("Near, weak", 720, 0, 3.3),
		hotel("Mid", 680, 0, 11.5),
		hotel("Tie, after", 740, 0, 7),
		hotel("Tie, before", 740, 0.001, 7),
	)
	var names []string
	for _, c := range res.Candidates {
		names = append(names, c.Name)
	}
	if len(names) != 3 || names[0] != "Tie, after" && names[0] != "Tie, before" {
		t.Fatalf("order %v", names)
	}
	if names[2] != "Near, weak" {
		t.Errorf("order %v: nearest to the budget point should come first", names)
	}
}
