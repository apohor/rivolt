package api

import (
	"fmt"
	"math"
	"time"
)

// Limits on what the plan endpoints accept before anything is planned. A
// departure is a time a person could plausibly pick: a re-plan or a trip that
// started up to two weeks ago, or one booked up to six months ahead. A
// departure more than farFutureAfter (30 days) ahead is planned in shifted time
// and never sent to Rivian as such (far_future.go).
const (
	maxPastDeparture   = 14 * 24 * time.Hour
	maxFutureDeparture = 180 * 24 * time.Hour
)

// planNow is the clock the plan endpoints validate departures against; tests
// pin it.
var planNow = time.Now

// departureWindowErr is non-nil when t is outside [now-14d, now+180d].
func departureWindowErr(t, now time.Time) error {
	if t.Before(now.Add(-maxPastDeparture)) || t.After(now.Add(maxFutureDeparture)) {
		return fmt.Errorf("must be within 14 days back and 180 days ahead")
	}
	return nil
}

// validLatLon reports a finite, in-range coordinate.
func validLatLon(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsNaN(lon) && !math.IsInf(lat, 0) && !math.IsInf(lon, 0) &&
		lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// validPct reports a finite percentage in (0, 100].
func validPct(v float64) bool { return !math.IsNaN(v) && v > 0 && v <= 100 }

// validTargetPct reports a finite arrival target in [0, 100].
func validTargetPct(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 100 }
