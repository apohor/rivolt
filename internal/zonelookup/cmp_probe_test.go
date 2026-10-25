package zonelookup

import (
	"testing"

	"github.com/bradfitz/latlong"
	"github.com/ringsaturn/tzf"
)

func TestCmpProbe(t *testing.T) {
	f, _ := tzf.NewDefaultFinder()
	cities := []struct {
		n        string
		lat, lon float64
	}{
		{"El Paso", 31.7619, -106.4850}, {"Pensacola", 30.4213, -87.2169}, {"Bristol TN", 36.5951, -82.1887}, {"Bowling Green KY", 36.9685, -86.4808},
		{"Evansville", 37.9716, -87.5711}, {"Gary", 41.5934, -87.3464}, {"Texarkana", 33.4251, -94.0477}, {"Wendover", 40.7391, -114.0372},
		{"Page AZ", 36.9147, -111.4558}, {"Kalispell", 48.1920, -114.3168}, {"Boise", 43.6150, -116.2023}, {"Lewiston ID", 46.4165, -117.0177},
		{"Nashville", 36.1627, -86.7816}, {"Chattanooga", 35.0456, -85.3097}, {"South Lake Tahoe", 38.9399, -119.9772}, {"Yuma", 32.6927, -114.6277},
		{"Bullhead City", 35.1478, -114.5683}, {"Laughlin", 35.1678, -114.5730}, {"Kingman", 35.1894, -114.0530}, {"Las Vegas", 36.1699, -115.1398},
		{"Salt Lake City", 40.7608, -111.8910}, {"Albuquerque", 35.0844, -106.6504}, {"Rapid City", 44.0805, -103.2310}, {"Bismarck", 46.8083, -100.7837},
		{"Sioux Falls", 43.5446, -96.7311}, {"Omaha", 41.2565, -95.9345}, {"Dallas", 32.7767, -96.7970}, {"Houston", 29.7604, -95.3698},
		{"Miami", 25.7617, -80.1918}, {"Atlanta", 33.7490, -84.3880}, {"Boston", 42.3601, -71.0589}, {"Portland OR", 45.5152, -122.6784},
		{"San Francisco", 37.7749, -122.4194}, {"San Diego", 32.7157, -117.1611}, {"Calgary", 51.0447, -114.0719}, {"Vancouver", 49.2827, -123.1207},
		{"Cheyenne", 41.1400, -104.8202}, {"Glenwood Springs", 39.5505, -107.3248}, {"Moab", 38.5733, -109.5498}, {"Flagstaff", 35.1983, -111.6513},
	}
	bad := 0
	for _, c := range cities {
		a, b := f.GetTimezoneName(c.lon, c.lat), latlong.LookupZoneName(c.lat, c.lon)
		if a != b {
			bad++
			t.Logf("DIFFER %-18s tzf=%-22s latlong=%s", c.n, a, b)
		}
	}
	t.Logf("%d of %d differ", bad, len(cities))
}
