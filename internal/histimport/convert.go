package histimport

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	kmToMi = 0.621371
	// A drive lasts at most a day, a charge three days (design §2).
	maxDriveDur  = 24 * time.Hour
	maxChargeDur = 72 * time.Hour
	maxSpeedMph  = 150
	maxEnergyKWh = 250
	maxPowerKW   = 400
	maxDistMi    = 3000
)

var errBadValue = errors.New("bad value")

// num parses a decimal with "." as the separator (ABRP writes "." in every locale; a "," is refused, not guessed). Empty is (0, false, nil).
func num(s string) (v float64, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "null") || strings.EqualFold(s, "nan") || s == "-" {
		return 0, false, nil
	}
	v, err = strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false, errBadValue
	}
	return v, true, nil
}

var timeLayouts = []string{
	time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04",
	"2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05 -0700", "2006-01-02 15:04:05 MST",
	"1/2/2006 15:04:05", "1/2/2006 15:04", "1/2/2006 3:04:05 PM", "1/2/2006 3:04 PM", "1/2/06 15:04", "1/2/06 3:04 PM",
}

// parsedTime is a time and whether the text carried its own zone.
type parsedTime struct {
	t      time.Time
	zoned  bool
	ambigu bool // a local time inside a DST fall-back hour (two instants), resolved to the first
}

// parseTime reads a timestamp. Text with an offset or Z is used as is. Text without one is read in loc, which the caller has confirmed with
// the user; with loc nil it reports needLoc. Numbers are Unix seconds or milliseconds (zoned) or an Excel day serial (not zoned).
func parseTime(s string, loc *time.Location) (p parsedTime, needLoc bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return p, false, errBadValue
	}
	if f, perr := strconv.ParseFloat(s, 64); perr == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		switch {
		case f >= 1e11 && f < 1e14:
			return parsedTime{t: time.UnixMilli(int64(f)).UTC(), zoned: true}, false, nil
		case f >= 1e8 && f < 1e11:
			return parsedTime{t: time.Unix(int64(f), 0).UTC(), zoned: true}, false, nil
		case f >= 20000 && f < 80000: // Excel serial day, 1899-12-30 based
			ms := int64(math.Round(f * 86400 * 1000))
			wall := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC).Add(time.Duration(ms) * time.Millisecond)
			return local(wall, loc)
		}
		return p, false, errBadValue
	}
	for _, l := range timeLayouts {
		t, perr := time.Parse(l, s)
		if perr != nil {
			continue
		}
		if hasZone(l) {
			return parsedTime{t: t.UTC(), zoned: true}, false, nil
		}
		return local(t, loc)
	}
	return p, false, errBadValue
}

func hasZone(layout string) bool {
	return strings.Contains(layout, "Z07") || strings.Contains(layout, "-0700") || strings.Contains(layout, "MST") || layout == time.RFC3339Nano
}

// local places a wall-clock time (read as UTC fields) in loc.
func local(wall time.Time, loc *time.Location) (parsedTime, bool, error) {
	if loc == nil {
		return parsedTime{}, true, nil
	}
	t := time.Date(wall.Year(), wall.Month(), wall.Day(), wall.Hour(), wall.Minute(), wall.Second(), wall.Nanosecond(), loc)
	// Inside a fall-back hour the same wall time happens twice; Go picks one. Flag it for the preview, do not drop it.
	amb := false
	const wallFmt = "2006-01-02 15:04:05"
	for _, d := range []time.Duration{-time.Hour, time.Hour} {
		if t.Add(d).Format(wallFmt) == t.Format(wallFmt) {
			amb = true
		}
	}
	return parsedTime{t: t.UTC(), ambigu: amb}, false, nil
}

// polyline5 encodes points as a Google polyline with precision 5, as live drives store theirs.
func polyline5(pts [][2]float64) string {
	var b []byte
	var pLat, pLon int
	for _, p := range pts {
		lat, lon := int(math.Round(p[0]*1e5)), int(math.Round(p[1]*1e5))
		b = appendVarint(b, lat-pLat)
		b = appendVarint(b, lon-pLon)
		pLat, pLon = lat, lon
	}
	return string(b)
}

func appendVarint(b []byte, v int) []byte {
	u := uint(v) << 1
	if v < 0 {
		u = ^u
	}
	for u >= 0x20 {
		b = append(b, byte((0x20|(u&0x1f))+63))
		u >>= 5
	}
	return append(b, byte(u+63))
}

// haversineMi is the great-circle distance in miles.
func haversineMi(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 3958.8
	p := math.Pi / 180
	dLat, dLon := (lat2-lat1)*p, (lon2-lon1)*p
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*p)*math.Cos(lat2*p)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}
