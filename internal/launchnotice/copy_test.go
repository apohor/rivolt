package launchnotice

import (
	"strings"
	"testing"
	"time"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNewParamsDerivesTheDatesFromTheLaunchDate(t *testing.T) {
	p := NewParams(day("2026-12-01"))
	if p.Launch != "2026-12-01" || p.FoundingEnd != "2027-12-01" || p.TermsEffective != "2026-12-31" {
		t.Errorf("%+v", p)
	}
	if err := p.Check(); err != nil {
		t.Error(err)
	}
	until, err := p.ShowsUntil()
	if err != nil || !until.Equal(day("2027-01-01")) {
		t.Errorf("shows until %v %v, want the end of the Terms day", until, err)
	}
}

func TestParamsCheckRefusesLessThan30DaysOfNotice(t *testing.T) {
	p := NewParams(day("2026-12-01"))
	p.TermsEffective = "2026-12-20"
	if p.Check() == nil {
		t.Error("19 days' notice accepted")
	}
	p.TermsEffective = "soon"
	if p.Check() == nil {
		t.Error("a malformed date accepted")
	}
}

func TestRenderedCopyHasNoPlaceholdersAndStatesTheDates(t *testing.T) {
	p := NewParams(day("2026-12-01"))
	body := RenderEmail(p)
	if tok := Unfilled(body); tok != "" {
		t.Errorf("email still has %q", tok)
	}
	for _, want := range []string{"December 1, 2026", "December 1, 2027", "December 31, 2026", "not affiliated with Rivian", "$34.99", "https://rivolt.dev/terms/2026-10-23", "shows one vehicle at a time", "Pro shows them all"} {
		if !strings.Contains(body, want) {
			t.Errorf("email is missing %q", want)
		}
	}
	for _, banned := range []string{"free forever", "limited beta", "invite-only", "early access", "Unsubscribe"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(banned)) {
			t.Errorf("email contains %q", banned)
		}
	}
	in := RenderInApp(p)
	if tok := Unfilled(in.Title + in.Body); tok != "" || !strings.Contains(in.Body, "December 31, 2026") || !strings.Contains(in.Body, "December 1, 2027") || !strings.Contains(in.Body, "one vehicle is shown at a time") || len(in.Links) != 2 {
		t.Errorf("in-app notice: %q %+v", tok, in)
	}
	if Unfilled("hello LAUNCH_DATE") != "LAUNCH_DATE" {
		t.Error("Unfilled misses a placeholder")
	}
}
