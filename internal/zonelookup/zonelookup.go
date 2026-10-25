// Package zonelookup names the IANA time zone at a coordinate, offline: the boundaries are embedded (ringsaturn/tzf), so no request
// leaves the process and no place is sent to anyone. The trip planner uses it so an arrival time can be shown in the destination's
// own clock.
package zonelookup

import (
	"math"
	"sync"
	"time"

	"github.com/ringsaturn/tzf"
)

var (
	once   sync.Once
	finder tzf.F
)

// For returns the IANA zone name at (lat, lon), such as "America/Denver". It returns "" for coordinates that are not on the globe
// (NaN, out of range, or the null island 0,0, which is what a missing coordinate looks like), for open water with no zone of its
// own, and for a name the runtime's time zone database cannot load, so a caller never has to guard a bad name. The first call loads
// the boundaries (about 150 ms); later calls take microseconds.
func For(lat, lon float64) string {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return ""
	}
	once.Do(func() {
		if f, err := tzf.NewDefaultFinder(); err == nil {
			finder = f
		}
	})
	if finder == nil {
		return ""
	}
	name := finder.GetTimezoneName(lon, lat)
	if name == "" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}
