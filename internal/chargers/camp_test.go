package chargers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The campground tags the archive build adds are read, and the camp filter
// matches a campground or RV park with a way to plug in, nothing else.
func TestCampgroundTagsAndFilter(t *testing.T) {
	file := archiveWithProps(t, []map[string]any{
		{"name": "Tule", "@id": "ridb/232709", "facility_type": "CAMPGROUND", "hookup_amps": 50, "electric_sites": 26, "sites": 104, "reservable": "yes", "source": "ridb", "ridb_id": "232709", "asof": "2026-10-20", "fee": "$30 per night"},
		{"name": "Pine RV", "@id": "nrel/1", "facility_type": "RV_PARK", "l2_count": 2, "socket:type1": "yes"},
		{"name": "Outlet only", "@id": "nrel/2", "facility_type": "CAMPGROUND", "socket:nema_1450": "yes"},
		{"name": "Tent camp, no power", "@id": "ridb/9", "facility_type": "CAMPGROUND"},
		{"name": "Hotel", "@id": "nrel/3", "facility_type": "HOTEL", "l2_count": 2},
		{"name": "Hotel with hookup tag", "@id": "nrel/4", "facility_type": "HOTEL", "hookup_amps": 50},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(file) }))
	defer srv.Close()
	a := New(srv.URL)
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	byName := map[string]POI{}
	for i := range 6 {
		pois, err := a.tilePOIs(14, uint32(1000+i), 6000)
		if err != nil || len(pois) != 1 {
			t.Fatalf("tile %d: %v %v", i, pois, err)
		}
		byName[pois[0].Name] = pois[0]
	}
	tule := byName["Tule"]
	if tule.HookupAmps != 50 || tule.ElectricSites != 26 || tule.Sites != 104 || !tule.Reservable ||
		tule.Source != "ridb" || tule.RIDBID != "232709" || tule.AsOf != "2026-10-20" || tule.EVPricing != "$30 per night" {
		t.Errorf("tags not read: %+v", tule)
	}
	if tule.IsL2 || tule.IsDCFC || tule.matches(FilterL2) || tule.matches(FilterDCFC) || tule.matches(FilterHotels) {
		t.Errorf("an RV hookup is not an EVSE: isL2=%v isDCFC=%v", tule.IsL2, tule.IsDCFC)
	}
	if !byName["Pine RV"].IsL2 || !byName["Pine RV"].matches(FilterL2) {
		t.Error("a listed Level 2 stall at a campground stays Level 2")
	}
	for name, want := range map[string]bool{
		"Tule": true, "Pine RV": true, "Outlet only": true,
		"Tent camp, no power": true, // no tags: the archive infers L2 for a station with no DC; the overnight rules drop it
		"Hotel":               false, "Hotel with hookup tag": false,
	} {
		if got := byName[name].matches(FilterCamp); got != want {
			t.Errorf("%s matches camp = %v, want %v", name, got, want)
		}
	}
}
