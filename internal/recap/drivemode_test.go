package recap

import "testing"

func TestHumanizeDriveMode(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		// Rivian wire values that the recorder persists.
		{"distance", "Conserve"},
		{"everyday", "All-Purpose"},
		{"sport", "Sport"},
		{"winter", "Snow"},
		{"towing", "Towing"},
		{"off_road_auto", "All-Terrain"},
		{"off_road_sand", "Soft Sand"},
		{"off_road_rocks", "Rock Crawl"},
		{"off_road_sport_auto", "Rally"},
		{"off_road_sport_drift", "Drift"},
		// Mixed case must collapse to the canonical wire value.
		{"Distance", "Conserve"},
		{" EVERYDAY ", "All-Purpose"},
		// Unknown values fall through to a Title-cased rendering so a
		// new Rivian mode still reads reasonably in the recap prompt
		// instead of leaking "off_road_dev_secret".
		{"off_road_dev_secret", "Off Road Dev Secret"},
		{"new-mode", "New Mode"},
	}
	for _, c := range cases {
		if got := humanizeDriveMode(c.in); got != c.want {
			t.Errorf("humanizeDriveMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
