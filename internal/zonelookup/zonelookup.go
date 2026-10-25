// Package zonelookup names the IANA time zone at a coordinate, offline: the boundaries are embedded (bradfitz/latlong), so no request
// leaves the process and no place is sent to anyone. The trip planner uses it so an arrival time can be shown in the destination's
// own clock.
//
// The boundary data is coarse and from 2017: it names the right zone for a city and for a place well inside a zone, and can be one
// zone off within a few kilometres of a zone border (Page AZ, Laughlin NV). A more exact finder (ringsaturn/tzf) costs about 55 MB of
// resident memory once loaded, against about 2 MB here; the prod pods run near their memory requests, so this trades border-town
// exactness for memory. Swap this one function if that changes.
package zonelookup

import (
	"math"
	"time"

	"github.com/bradfitz/latlong"
)

// For returns the IANA zone name at (lat, lon), such as "America/Denver". It returns "" for coordinates that are not on the globe
// (NaN, out of range, or the null island 0,0, which is what a missing coordinate looks like), for open water with no zone of its
// own, and for a name the runtime's time zone database cannot load, so a caller never has to guard a bad name.
func For(lat, lon float64) string {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 || (lat == 0 && lon == 0) {
		return ""
	}
	name := latlong.LookupZoneName(lat, lon)
	if name == "" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}
