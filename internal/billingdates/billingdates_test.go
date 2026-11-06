package billingdates

import (
	"testing"
	"time"
)

func TestBasisFor(t *testing.T) {
	for source, want := range map[string]string{
		"": "", "stripe": UTCDate, "founding": UTCDate, "trial": UTCDate, "grant": UTCDate, "billing_off": UTCDate, "demo": UTCDate, "apple": Instant,
	} {
		if got := BasisFor(source); got != want {
			t.Errorf("BasisFor(%q) = %q, want %q", source, got, want)
		}
	}
}

// The date is the UTC calendar date of the instant, whatever zone the time value carries: the same instant in four zones is one date.
func TestDateIsTheUTCCalendarDateInAnyZone(t *testing.T) {
	instant := time.Date(2027, 11, 6, 2, 52, 0, 0, time.UTC) // 02:52Z: Nov 5 in Pacific and Hawaii, Nov 6 in UTC and Tokyo
	for _, zone := range []string{"America/Los_Angeles", "Pacific/Honolulu", "Asia/Tokyo", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no tzdata for %s", zone)
		}
		local := instant.In(loc)
		if got := Date(&local); got == nil || *got != "2027-11-06" {
			t.Errorf("%s: %v, want 2027-11-06", zone, got)
		}
	}
	if Date(nil) != nil {
		t.Error("nil must stay nil")
	}
}
