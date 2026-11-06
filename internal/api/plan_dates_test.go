package api

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/entitlements"
)

func tp(y int, m time.Month, d, h, mi int) *time.Time {
	t := time.Date(y, m, d, h, mi, 0, 0, time.UTC)
	return &t
}

func strOf(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// B8: a Stripe, founding, trial or grant period carries its UTC calendar dates (a 02:52Z instant is Nov 6, whatever the viewer's zone); an Apple
// period is an instant the device formats, with no *_date field; an account that never had Pro has neither.
func TestPlanFieldsCarryTheUTCDatesPerSource(t *testing.T) {
	instant := tp(2027, 11, 6, 2, 52)
	for _, source := range []string{"stripe", "founding", "trial", "grant", "billing_off"} {
		pf := planFromState(entitlements.State{Pro: true, Source: source, PeriodEnd: instant, TrialEnds: instant, CancelsAt: instant, GraceEnds: instant}, true)
		if strOf(pf.PlanDateBasis) != "utc_date" {
			t.Errorf("%s: basis %s, want utc_date", source, strOf(pf.PlanDateBasis))
		}
		for name, got := range map[string]*string{"period_end": pf.PlanPeriodEndDate, "trial_ends": pf.PlanTrialEndsDate, "cancels": pf.PlanCancelsDate, "grace_ends": pf.PlanGraceEndsDate} {
			if strOf(got) != "2027-11-06" {
				t.Errorf("%s %s date %s, want 2027-11-06", source, name, strOf(got))
			}
		}
		if pf.PlanEndedDate != nil {
			t.Errorf("%s: an ended date on a Pro account: %s", source, strOf(pf.PlanEndedDate))
		}
	}
	// the instants stay as they were
	pf := planFromState(entitlements.State{Pro: true, Source: "stripe", PeriodEnd: instant}, true)
	if pf.PlanPeriodEnd == nil || !pf.PlanPeriodEnd.Equal(*instant) {
		t.Errorf("the period end instant changed: %v", pf.PlanPeriodEnd)
	}
}

func TestPlanFieldsAppleStaysAnInstant(t *testing.T) {
	pf := planFromState(entitlements.State{Pro: true, Source: "apple", PeriodEnd: tp(2027, 11, 6, 2, 52), CancelsAt: tp(2027, 11, 6, 2, 52)}, true)
	if strOf(pf.PlanDateBasis) != "instant" {
		t.Errorf("basis %s, want instant", strOf(pf.PlanDateBasis))
	}
	if pf.PlanPeriodEndDate != nil || pf.PlanCancelsDate != nil || pf.PlanTrialEndsDate != nil || pf.PlanGraceEndsDate != nil || pf.PlanEndedDate != nil {
		t.Errorf("an Apple plan must carry no *_date field: %+v", pf)
	}
	if pf.PlanPeriodEnd == nil {
		t.Error("the instant must stay")
	}
}

// A lapsed account takes the basis from the source that lapsed: "Pro ended on ..." for Stripe is the UTC date (the refund of rehearsal 2 was Nov 6
// UTC and read Nov 5 in Pacific), for Apple an instant; an account that never had Pro has no basis.
func TestPlanFieldsLapsedAndNeverHadPro(t *testing.T) {
	pf := planFromState(entitlements.State{Pro: false, EndedAt: tp(2026, 11, 6, 2, 55), EndedSource: "stripe"}, true)
	if strOf(pf.PlanDateBasis) != "utc_date" || strOf(pf.PlanEndedDate) != "2026-11-06" {
		t.Errorf("lapsed stripe: basis %s ended %s", strOf(pf.PlanDateBasis), strOf(pf.PlanEndedDate))
	}
	pf = planFromState(entitlements.State{Pro: false, EndedAt: tp(2026, 11, 6, 2, 55), EndedSource: "apple"}, true)
	if strOf(pf.PlanDateBasis) != "instant" || pf.PlanEndedDate != nil {
		t.Errorf("lapsed apple: basis %s ended %s", strOf(pf.PlanDateBasis), strOf(pf.PlanEndedDate))
	}
	pf = planFromState(entitlements.State{Pro: false}, true)
	if pf.PlanDateBasis != nil || pf.PlanEndedDate != nil || pf.PlanPeriodEndDate != nil {
		t.Errorf("never had Pro: %+v", pf)
	}
}
