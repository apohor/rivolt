package dcfcrates

import (
	"strings"
	"testing"
	"time"
)

func TestObservedRowsAreSane(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range observedPrices {
		if seen[o.Slug] {
			t.Errorf("%s listed twice", o.Slug)
		}
		seen[o.Slug] = true
		if _, ok := networkBySlug(o.Slug); !ok {
			t.Errorf("%s is not a network in the table", o.Slug)
		}
		if o.Low <= 0 || o.High < o.Low || o.High > 2 {
			t.Errorf("%s: implausible %v..%v", o.Slug, o.Low, o.High)
		}
		if _, err := time.Parse("2006-01", o.Month); err != nil {
			t.Errorf("%s: month %q: %v", o.Slug, o.Month, err)
		}
		if o.URL == "" || o.Note == "" {
			t.Errorf("%s: a row needs its source link and a note", o.Slug)
		}
	}
	// Networks growth found no figure for must stay absent: the planner says the price
	// is shown at the charger.
	for _, slug := range []string{"ionna", "chargepoint", "shell_recharge", "flo", "ev_connect"} {
		if seen[slug] {
			t.Errorf("%s has no observed figure and must not get one", slug)
		}
	}
}

func TestObservedLabelIsTheApprovedWording(t *testing.T) {
	o, ok := ObservedFor("evgo", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("no figure")
	}
	want := "Typical price seen by DCFC Tracker, Oct 2026. The price at the charger may differ."
	if got := o.Label(); got != want {
		t.Errorf("label = %q, want %q", got, want)
	}
}

func TestObservedHidesAtTwelveMonths(t *testing.T) {
	for now, want := range map[time.Time]bool{
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC):    true,
		time.Date(2027, 9, 30, 23, 59, 59, 0, time.UTC): true,
		time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC):    false, // the month, 12 months on
		time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC):     false,
	} {
		if _, ok := ObservedFor("ea", now); ok != want {
			t.Errorf("at %s: shown = %v, want %v", now.Format("2006-01-02"), ok, want)
		}
	}
	for _, slug := range []string{"ionna", "chargepoint", "nope", ""} {
		if _, ok := ObservedFor(slug, time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)); ok {
			t.Errorf("%q has a figure", slug)
		}
	}
}

func TestObservedTeslaIsARange(t *testing.T) {
	o, _ := ObservedFor("tesla_sc", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if !(o.Low < o.High) {
		t.Errorf("tesla: %v..%v; the membership and non-member prices should make a range", o.Low, o.High)
	}
	r, _ := ObservedFor("ran", time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC))
	if r.Low != r.High {
		t.Errorf("ran: %v..%v; one automatic Rivian-vehicle average", r.Low, r.High)
	}
}

// The source link is rendered as a link in the UI and is the only URL the restore
// accepts inside a saved plan: every row's must be https on the tracker's own host.
func TestObservedURLsAreHTTPSOnTheTrackersHost(t *testing.T) {
	for _, o := range observedPrices {
		if !strings.HasPrefix(o.URL, "https://dcfctracker.com/") || strings.ContainsAny(o.URL, " \t\r\n\"'<>") {
			t.Errorf("%s: source URL %q", o.Slug, o.URL)
		}
	}
}
