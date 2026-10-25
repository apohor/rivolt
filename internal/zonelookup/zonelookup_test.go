package zonelookup

import (
	"math"
	"testing"
)

func TestFor(t *testing.T) {
	for _, c := range []struct {
		name     string
		lat, lon float64
		want     string
	}{
		{"Denver", 39.7392, -104.9903, "America/Denver"},
		{"Seattle", 47.6205, -122.3493, "America/Los_Angeles"},
		{"Spokane", 47.6588, -117.4260, "America/Los_Angeles"},
		{"Chicago", 41.8781, -87.6298, "America/Chicago"},
		{"New York", 40.7128, -74.0060, "America/New_York"},
		{"Phoenix, no DST", 33.4484, -112.0740, "America/Phoenix"},
		{"Anchorage", 61.2181, -149.9003, "America/Anchorage"},
		{"Honolulu", 21.3069, -157.8583, "Pacific/Honolulu"},
		{"Toronto", 43.6532, -79.3832, "America/Toronto"},
		{"Tokyo", 35.6762, 139.6503, "Asia/Tokyo"},
	} {
		if got := For(c.lat, c.lon); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestForRefusesWhatIsNotACoordinate(t *testing.T) {
	for _, c := range [][2]float64{{math.NaN(), 10}, {10, math.NaN()}, {91, 0}, {0, 181}, {-91, 10}, {10, -181}, {0, 0}} {
		if got := For(c[0], c[1]); got != "" {
			t.Errorf("(%v, %v): %q, want empty", c[0], c[1], got)
		}
	}
}
