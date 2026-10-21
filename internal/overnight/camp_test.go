package overnight

import (
	"errors"
	"testing"

	"github.com/apohor/rivolt/internal/chargers"
)

func camp(name string, km float64, p chargers.POI) chargers.POI {
	p.Name, p.EntityID = name, "ridb/"+name
	p.Lat, p.Lon = 34+km/111.195, -118
	p.FacilityType = "CAMPGROUND"
	return p
}

func TestCampgroundsAreOptIn(t *testing.T) {
	a := &fakeArchive{
		hotels: []chargers.POI{hotel("H", 800, 0.5, 7)},
		camps:  []chargers.POI{camp("C", 800, chargers.POI{HookupAmps: 50, RIDBID: "232709", Source: "ridb", Reservable: true})},
	}
	// No kinds: hotels only, as before, and campgrounds are not even asked for.
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8})
	if err != nil || len(res.Candidates) != 1 || res.Candidates[0].Kind != KindHotel {
		t.Fatalf("default: %+v %v", res.Candidates, err)
	}
	res, err = Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8, Kinds: []string{KindHotel, KindCampground}})
	if err != nil || len(res.Candidates) != 2 {
		t.Fatalf("both: %+v %v", res.Candidates, err)
	}
	if _, err := Suggest(a, Request{Path: route(1000), Kinds: []string{"castle"}}); !errors.Is(err, ErrBadRequest) {
		t.Errorf("unknown kind: %v, want ErrBadRequest", err)
	}
}

func TestCampgroundHookupRules(t *testing.T) {
	a := &fakeArchive{camps: []chargers.POI{
		camp("fifty", 800, chargers.POI{HookupAmps: 50, ElectricSites: 26, Sites: 104, RIDBID: "232709", Source: "ridb", Reservable: true, AsOf: "2026-10-20", EVPricing: "$30 per night"}),
		camp("thirty", 801, chargers.POI{HookupAmps: 30, RIDBID: "232710", Source: "ridb"}),
		camp("twenty", 802, chargers.POI{HookupAmps: 20, RIDBID: "232711", Source: "ridb"}),
		camp("stall", 803, chargers.POI{IsL2: true, L2Count: 2, SocketJ1772: true}),
		camp("nrel outlet", 804, chargers.POI{SocketNEMA1450: true}),
		camp("inferred only", 805, chargers.POI{IsL2: true}), // the archive infers L2 for a station with no DC; nothing says it has a stall
		camp("bad id", 806, chargers.POI{HookupAmps: 50, RIDBID: "../x", Source: "ridb"}),
	}}
	res, err := Suggest(a, Request{Path: route(1000), DriveSec: 10 * 3600, BudgetHours: 8, Kinds: []string{KindCampground}})
	if err != nil {
		t.Fatal(err)
	}
	// Only three are returned; ask for the details by name from the full set.
	all := map[string]Candidate{}
	for _, p := range a.camps {
		if c, ok := campCandidate(p); ok {
			all[c.Name] = c
		}
	}
	if len(res.Candidates) != 3 {
		t.Errorf("%d candidates, want the three-best cap", len(res.Candidates))
	}
	f := all["fifty"]
	if f.Connector != ConnNEMA1450 || f.L2KW != 7.2 || !f.L2KWAssumed || f.NeedsAdapter || f.Kind != KindCampground ||
		f.ElectricSites != 26 || f.Sites != 104 || f.Fee != "$30 per night" || f.AsOf != "2026-10-20" || f.Source != "Recreation.gov" ||
		f.SourceURL != "https://www.recreation.gov/camping/campgrounds/232709" || f.Attribution != Attribution ||
		f.ReservationRequired == nil || !*f.ReservationRequired {
		t.Errorf("50 A site: %+v", f)
	}
	th := all["thirty"]
	if th.Connector != ConnTT30 || th.L2KW != 1.4 || !th.NeedsAdapter || th.ReservationRequired == nil || *th.ReservationRequired {
		t.Errorf("30 A site: %+v", th)
	}
	if _, ok := all["twenty"]; ok {
		t.Error("a 20 A (120 V) site must not be offered")
	}
	if s := all["stall"]; s.Connector != ConnJ1772 || s.L2KW != 7 || !s.L2KWAssumed || s.SourceURL != "" || s.ReservationRequired != nil {
		t.Errorf("NREL stall: %+v", s)
	}
	if o := all["nrel outlet"]; o.Connector != ConnNEMA1450 || o.L2KW != 7.2 {
		t.Errorf("NREL NEMA 14-50: %+v", o)
	}
	if _, ok := all["inferred only"]; ok {
		t.Error("a campground with no stall, hookup or outlet must not be offered")
	}
	if b := all["bad id"]; b.SourceURL != "" || b.Attribution != "" || b.Connector != ConnNEMA1450 {
		t.Errorf("a non-numeric id must give no link: %+v", b)
	}
	// A 7+ kW stay ranks above the 1.4 kW 30 A site.
	if res.Candidates[0].L2KW < 7 {
		t.Errorf("first candidate is %+v, want a 7 kW stay", res.Candidates[0])
	}
}
