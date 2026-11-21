package entitlements

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestGrantEndBounds(t *testing.T) {
	now := time.Date(2026, 11, 21, 15, 30, 0, 0, time.UTC)
	end, err := GrantEnd("2027-11-21", now)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2027, 11, 21, 23, 59, 59, 0, time.UTC); !end.Equal(want) {
		t.Errorf("end = %s, want the last second of the day %s", end, want)
	}
	// 400 days from 2026-11-21 is 2027-12-26: the last day allowed.
	if _, err := GrantEnd("2027-12-26", now); err != nil {
		t.Errorf("day 400 refused: %v", err)
	}
	for name, in := range map[string]string{
		"today":          "2026-11-21",
		"yesterday":      "2026-11-20",
		"a year ago":     "2025-11-21",
		"day 401":        "2027-12-27",
		"far ahead":      "2030-01-01",
		"not a date":     "next year",
		"with a time":    "2027-01-01T10:00:00Z",
		"empty":          "",
		"month and year": "2027-01",
	} {
		if _, err := GrantEnd(in, now); err == nil {
			t.Errorf("%s (%q) was accepted", name, in)
		}
	}
}

func TestGrantReason(t *testing.T) {
	if r, err := CheckGrantReason("  creator program, Nov 2026 \n"); err != nil || r != "creator program, Nov 2026" {
		t.Errorf("good reason: %q, %v", r, err)
	}
	for name, in := range map[string]string{
		"empty":     "   ",
		"too long":  strings.Repeat("x", grantReasonMax+1),
		"an email":  "comp for jane@example.com",
		"two lines": "one\ntwo",
		"a control": "bell\x07",
	} {
		if _, err := CheckGrantReason(in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestMaskEmailNeverShowsTheAddress(t *testing.T) {
	for in, want := range map[string]string{
		"jane.doe@example.com":    "j***@e***.com",
		"X@Y.org":                 "X***@Y***.org",
		"someone@mail.example.de": "s***@m***.de",
		"nodomain":                "***",
		"@x.com":                  "***",
	} {
		got := MaskEmail(in)
		if got != want {
			t.Errorf("MaskEmail(%q) = %q, want %q", in, got, want)
		}
		if strings.Contains(got, "doe") || strings.Contains(got, "example") {
			t.Errorf("MaskEmail(%q) = %q leaks", in, got)
		}
	}
}

func TestCheckGrant(t *testing.T) {
	day := 24 * time.Hour
	end := t0.Add(100 * day)
	ok := func(name string, rows ...Row) {
		t.Helper()
		if err := CheckGrant(rows, end, t0); err != nil {
			t.Errorf("%s: refused: %v", name, err)
		}
	}
	no := func(name, said string, rows ...Row) {
		t.Helper()
		err := CheckGrant(rows, end, t0)
		if !errors.Is(err, ErrGrantCovered) {
			t.Fatalf("%s: err = %v, want ErrGrantCovered", name, err)
		}
		if !strings.Contains(err.Error(), said) {
			t.Errorf("%s: %q does not say what it found (%q)", name, err, said)
		}
	}
	ok("a free account")
	ok("an ended paid plan", Row{Source: SourceStripe, Status: StatusEnded, PeriodEnd: at(-day)})
	no("a founding year that ends sooner", "founding Pro", Row{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(50 * day)})
	ok("a lapsed founding year", Row{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(-day)})
	ok("an earlier grant (it is extended)", Row{Source: SourceGrant, Status: StatusActive, PeriodEnd: at(10 * day)})
	ok("an ended grant", Row{Source: SourceGrant, Status: StatusEnded, PeriodEnd: at(300 * day)})

	no("a running Stripe plan, whatever its end", "paid stripe plan", Row{Source: SourceStripe, Status: StatusActive, PeriodEnd: at(20 * day)})
	no("a running Apple plan", "paid apple plan", Row{Source: SourceApple, Status: StatusActive, PeriodEnd: at(20 * day)})
	no("a past-due plan in its grace", "paid stripe plan", Row{Source: SourceStripe, Status: StatusPastDue, PeriodEnd: at(-2 * day)})
	no("a cancelled plan still running", "paid stripe plan", Row{Source: SourceStripe, Status: StatusActive, PeriodEnd: at(5 * day), CancelAtPeriodEnd: true})
	no("a founding year that ends later", "founding Pro", Row{Source: SourceFounding, Status: StatusActive, PeriodEnd: at(200 * day)})
	no("a founding year that ends the same second", "founding Pro", Row{Source: SourceFounding, Status: StatusActive, PeriodEnd: &end})
	no("a grant that ends later", "grant Pro", Row{Source: SourceGrant, Status: StatusActive, PeriodEnd: at(150 * day)})
	no("a grant without an end", "no end", Row{Source: SourceGrant, Status: StatusActive})
}

// A granted account reads as Pro until its end and as free from the next second, with the lapse reported (nothing is deleted: the row stays,
// and the plan logic only stops counting it).
func TestGrantedAccountIsProUntilTheEndDate(t *testing.T) {
	end := time.Date(2027, 3, 4, 23, 59, 59, 0, time.UTC)
	rows := []Row{{Source: SourceGrant, Status: StatusActive, PeriodEnd: &end}}
	if st := Compute(rows, end.Add(-time.Second)); !st.Pro || st.Source != SourceGrant || st.PeriodEnd == nil || !st.PeriodEnd.Equal(end) {
		t.Errorf("before the end: %+v", st)
	}
	dayAfter := time.Date(2027, 3, 5, 12, 0, 0, 0, time.UTC)
	st := Compute(rows, dayAfter)
	if st.Pro || st.EndedAt == nil || !st.EndedAt.Equal(end) || st.EndedSource != SourceGrant {
		t.Errorf("the day after: %+v, want free with the grant's end as the lapse", st)
	}
	// A revoked grant (status ended) is free at once, whatever its date, and reports when it was revoked.
	revoked := []Row{{Source: SourceGrant, Status: StatusEnded, PeriodEnd: &end, UpdatedAt: t0}}
	if st := Compute(revoked, t0.Add(time.Hour)); st.Pro || st.EndedSource != SourceGrant || st.EndedAt == nil || !st.EndedAt.Equal(t0) {
		t.Errorf("revoked: %+v", st)
	}
}

// A Stripe subscription started during a grant is the plan (it ranks above the grant) and the grant's end is what the checkout starts
// billing at, so the two never charge for the same months; after the grant ends the subscription alone keeps Pro.
func TestSubscriptionDuringAGrantTakesPrecedence(t *testing.T) {
	day := 24 * time.Hour
	grantEnd := t0.Add(60 * day)
	subEnd := t0.Add(400 * day)
	grant := Row{Source: SourceGrant, Status: StatusActive, PeriodEnd: &grantEnd}
	sub := Row{Source: SourceStripe, Status: StatusActive, PeriodEnd: &subEnd}
	if free, ok := ActiveFreeEnd([]Row{grant}, t0); !ok || !free.Equal(grantEnd) {
		t.Errorf("free end before subscribing = %v %v, want the grant's end (billing starts there)", free, ok)
	}
	st := Compute([]Row{grant, sub}, t0)
	if !st.Pro || st.Source != SourceStripe || !st.PeriodEnd.Equal(subEnd) {
		t.Errorf("during the grant, with a subscription: %+v, want the subscription", st)
	}
	if st := Compute([]Row{grant, sub}, grantEnd.Add(day)); !st.Pro || st.Source != SourceStripe {
		t.Errorf("after the grant: %+v, want the subscription alone to keep Pro", st)
	}
	// No lapse is reported while a subscription runs, and the grant never makes a subscription look like a trial.
	if st := Compute([]Row{grant, sub}, grantEnd.Add(day)); st.TrialEnds != nil || st.EndedAt != nil {
		t.Errorf("trial/lapse fields: %+v", st)
	}
}
