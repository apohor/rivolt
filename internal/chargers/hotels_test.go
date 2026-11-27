package chargers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The hotels filter must know the lodging facility types NREL actually writes.
// The live archive has B_AND_B (83 stations), not BED_AND_BREAKFAST, which an
// earlier list used and so missed them.
func TestHotelsFilterTypes(t *testing.T) {
	for _, ft := range []string{"HOTEL", "INN", "B_AND_B", "b_and_b", "MOTEL", "LODGE", "RESORT"} {
		if !(POI{FacilityType: ft, IsL2: true}).matches(FilterHotels) {
			t.Errorf("%s with L2 should match the hotels filter", ft)
		}
	}
	if (POI{FacilityType: "B_AND_B"}).matches(FilterHotels) {
		t.Error("a hotel without L2 must not match")
	}
	for _, ft := range []string{"", "CAMPGROUND", "PARKING_LOT", "RESTAURANT"} {
		if (POI{FacilityType: ft, IsL2: true}).matches(FilterHotels) {
			t.Errorf("%q must not match the hotels filter", ft)
		}
	}
}

// Loading the archive collects the hotel stations (the "hotels" rule: a lodging type with a Level 2 stall) once each, with their address, for
// the place search; the rest of the archive is not in the list.
func TestArchiveHotelsListedAtLoad(t *testing.T) {
	body := archiveWithProps(t, []map[string]any{
		{"name": "Imbue Investments Hampton Inn Dalhart", "@id": "nrel/1", "facility_type": "HOTEL", "l2_count": 2, "city": "Dalhart", "state": "TX", "country": "US"},
		{"name": "Motel With Only A Fast Charger", "@id": "nrel/2", "facility_type": "MOTEL", "dcfc_count": 2, "city": "Dumas", "state": "TX"},
		{"name": "Supermarket", "@id": "nrel/3", "facility_type": "GROCERY", "l2_count": 2},
		{"name": "Old Archive Inn", "@id": "nrel/4", "facility_type": "INN", "l2_count": 1},
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	a := New(srv.URL)
	if h, _ := a.Hotels(); len(h) != 0 {
		t.Fatalf("before load: %d hotels", len(h))
	}
	if err := a.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	hotels, at := a.Hotels()
	if len(hotels) != 2 || at.IsZero() {
		t.Fatalf("hotels=%d at=%v, want the two L2 lodgings", len(hotels), at)
	}
	by := map[string]POI{}
	for _, h := range hotels {
		by[h.Name] = h
	}
	if h := by["Imbue Investments Hampton Inn Dalhart"]; h.City != "Dalhart" || h.State != "TX" || h.Country != "US" {
		t.Errorf("Dalhart hotel = %+v", h)
	}
	// An archive built before the address props existed still loads; the hotel has no city.
	if h, ok := by["Old Archive Inn"]; !ok || h.City != "" {
		t.Errorf("old-archive hotel = %+v ok=%v", h, ok)
	}
	var nilArchive *Archive
	if h, _ := nilArchive.Hotels(); h != nil {
		t.Error("nil archive")
	}
}
