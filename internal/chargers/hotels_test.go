package chargers

import "testing"

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
