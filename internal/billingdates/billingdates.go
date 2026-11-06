// Package billingdates decides which calendar date a billing instant is shown as (B8, DA's Pro-flip blocker).
//
// One instant used to show two dates: Stripe's Portal and emails state the UTC calendar date ("Cancels Nov 6, 2027") while our cards formatted the
// same instant in the viewer's zone ("ends November 5, 2027" in Pacific). Matching the processor is the point, so a period that Stripe, our own
// founding grant, a trial or a grant defines is shown as its UTC calendar date, as a plain date with no zone conversion and no "UTC" suffix. An
// Apple period is shown by Apple in the device's zone, so ours stays an instant the client formats on the device.
package billingdates

import "time"

// The two bases of /api/auth/me's plan_date_basis.
const (
	// UTCDate: the plan_*_date fields carry the UTC calendar date of each instant; show them as dates, with no zone conversion.
	UTCDate = "utc_date"
	// Instant: Apple's own UI shows device-local dates, so the instants are formatted on the device and no *_date field is set.
	Instant = "instant"
)

// BasisFor is the basis for a plan source (stripe, apple, founding, grant, trial, billing_off, demo): "" for no source.
func BasisFor(source string) string {
	switch source {
	case "":
		return ""
	case "apple":
		return Instant
	}
	return UTCDate
}

// Date is the UTC calendar date of t as "YYYY-MM-DD" (the zone t carries is irrelevant), nil for nil.
func Date(t *time.Time) *string {
	if t == nil {
		return nil
	}
	d := t.UTC().Format("2006-01-02")
	return &d
}
