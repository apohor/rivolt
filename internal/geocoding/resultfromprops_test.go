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
