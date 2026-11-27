package geocoding

import "testing"

func TestResultFromPropsLocality(t *testing.T) {
	cases := []struct {
		name     string
		p        photonProps
		wantName string
		wantLoc  string
	}{
		{"house number", photonProps{HouseNumber: "12", Street: "Main St", City: "Austin"}, "12 Main St, Austin", "Austin"},
		{"street", photonProps{Street: "Main St", City: "Austin"}, "Main St, Austin", "Austin"},
		{"city", photonProps{Name: "Austin", City: "Austin"}, "Austin", ""},
		{"park keeps its own name", photonProps{Name: "Yosemite National Park", City: "Mariposa"}, "Yosemite National Park", ""},
		{"street without a city", photonProps{Street: "Main St"}, "Main St", ""},
	}
	for _, c := range cases {
		r := resultFromProps(c.p, 1, 2)
		if r.Name != c.wantName || r.Locality != c.wantLoc {
			t.Errorf("%s: got name %q locality %q, want %q %q", c.name, r.Name, r.Locality, c.wantName, c.wantLoc)
		}
	}
}

// A text search names a POI by itself and its town; Reverse keeps the address. A street or a place keeps its old label.
func TestSearchResultFromPropsPOI(t *testing.T) {
	hotel := photonProps{Name: "Hampton Inn & Suites", HouseNumber: "1415", Street: "West Wilson Street", City: "Borger", State: "Texas", OSMKey: "tourism", OSMValue: "hotel", Type: "house"}
	cases := []struct {
		name     string
		p        photonProps
		wantName string
		wantLoc  string
	}{
		{"hotel gains its town", hotel, "Hampton Inn & Suites, Borger", "Borger"},
		{"name already holds the town", photonProps{Name: "Hampton Inn & Suites Dumas", Street: "S Dumas Ave", City: "Dumas", OSMKey: "tourism", OSMValue: "hotel", Type: "house"}, "Hampton Inn & Suites Dumas", "Dumas"},
		{"POI without a town", photonProps{Name: "Boston Cemetery", OSMKey: "landuse", OSMValue: "cemetery", Type: "other"}, "Boston Cemetery", ""},
		{"city is not a POI", photonProps{Name: "Dallas", OSMKey: "place", OSMValue: "city", Type: "city"}, "Dallas", ""},
		{"named street stays a street", photonProps{Name: "Main Street", City: "Clayton", OSMKey: "highway", OSMValue: "trunk", Type: "street"}, "Main Street", ""},
		{"plain address", photonProps{HouseNumber: "12", Street: "Main St", City: "Austin", OSMKey: "place", OSMValue: "house", Type: "house"}, "12 Main St, Austin", "Austin"},
	}
	for _, c := range cases {
		r := searchResultFromProps(c.p, 1, 2)
		if r.Name != c.wantName || r.Locality != c.wantLoc {
			t.Errorf("%s: got %q / %q, want %q / %q", c.name, r.Name, r.Locality, c.wantName, c.wantLoc)
		}
	}
	// Reverse lookups still name the address, never a business.
	if r := resultFromProps(hotel, 1, 2); r.Name != "1415 West Wilson Street, Borger" {
		t.Errorf("resultFromProps changed: %q", r.Name)
	}
}
