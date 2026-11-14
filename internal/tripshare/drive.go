package tripshare

import "errors"

// ErrNoRoute is a drive with no route line to share.
var ErrNoRoute = errors.New("tripshare: drive has no route")

// DriveTrim is a finished drive's route cut the way a shared plan is: both ends and the saved home cut away by straight-line distance,
// the ends rounded to an area. Nothing here can be walked back to an address.
type DriveTrim struct {
	Start, End Area
	Segments   []string // polyline6 runs outside every private circle
}

// TrimDrive cuts a drive's stored route (lat, lon points) with the same circles and radius rule as Build: the drive's two ends and the
// saved home, at the radius EffectiveCut gives. ErrNoRoute when the line has fewer than two points, ErrTooShort when too little remains.
func TrimDrive(line [][2]float64, home *Circle, cutM float64) (*DriveTrim, error) {
	if len(line) < 2 {
		return nil, ErrNoRoute
	}
	r := EffectiveCut(cutM, home)
	first, last := line[0], line[len(line)-1]
	circles := []Circle{{first[0], first[1], r}, {last[0], last[1], r}}
	if home != nil {
		circles = append(circles, Circle{home.Lat, home.Lon, r})
	}
	segs, _ := Cut(line, circles)
	kept := 0.0
	for _, s := range segs {
		kept += s.ToM - s.FromM
	}
	if kept < minKeptM {
		return nil, ErrTooShort
	}
	out := &DriveTrim{
		Start: Area{"Start area", roundArea(first[0]), roundArea(first[1])},
		End:   Area{"Destination area", roundArea(last[0]), roundArea(last[1])},
	}
	for _, s := range segs {
		out.Segments = append(out.Segments, s.Encode())
	}
	return out, nil
}
