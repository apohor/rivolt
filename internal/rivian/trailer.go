package rivian

import "time"

// Trailer presence on drives (Sprint 8 item 7). The signal is the trailer connector (body.trailer.state), not the
// load: a bike rack or a cargo carrier with lights plugged into the 7-pin can read "present". Only the Go side names
// it a trailer; the user-facing wording is "Trailer" / "Trailer connected".

// Trailer states carried in State.TrailerState. "" is unknown.
const (
	TrailerPresent = "present"
	TrailerAbsent  = "not_present"
)

// trailerStateFromParallax maps body.trailer.state's presence enum to State.TrailerState: "" for unspecified and
// invalid (leave what is known), "present" for present and present with brakes.
func trailerStateFromParallax(v uint64) string {
	switch v {
	case 1:
		return TrailerAbsent
	case 2, 3:
		return TrailerPresent
	default:
		return ""
	}
}

// trailerAcc integrates how long the trailer was connected over a drive. It carries the last known value forward:
// the topic may send only on change (one "present" at hitch-up, then silence), so counting only the time between
// frames would give an empty or arbitrary answer. Time before the first known value is not counted.
type trailerAcc struct {
	present time.Duration // time the trailer was connected
	known   time.Duration // time the value was known
	last    string        // last known value: TrailerPresent, TrailerAbsent, or "" while never known
	lastAt  time.Time     // when the clock last advanced
}

// observe advances the clock to at (counting the span since the last call at the last known value) and then takes
// v as the new last known value when it is known.
func (a *trailerAcc) observe(at time.Time, v string) {
	if !a.lastAt.IsZero() && at.After(a.lastAt) {
		if d := at.Sub(a.lastAt); a.last != "" {
			a.known += d
			if a.last == TrailerPresent {
				a.present += d
			}
		}
	}
	if at.After(a.lastAt) {
		a.lastAt = at
	}
	if v != "" {
		a.last = v
	}
}

// result is the drive's trailer flag as of at: true when the trailer was connected for at least half of the time the
// value was known, false when it was known and connected for less, nil when it was never known. It does not change
// the accumulator, so a mid-drive snapshot is cheap and the final one agrees.
func (a trailerAcc) result(at time.Time) *bool {
	c := a
	c.observe(at, "")
	if c.known <= 0 && c.last == "" {
		return nil
	}
	if c.known <= 0 {
		// Known only at the very end (a single instant): the value itself.
		v := c.last == TrailerPresent
		return &v
	}
	v := c.present*2 >= c.known
	return &v
}
