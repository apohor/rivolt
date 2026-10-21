package dcfcrates

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestMembershipTermsAreSourced(t *testing.T) {
	hosts := map[string]bool{"www.electrifyamerica.com": true, "www.tesla.com": true, "www.evgo.com": true, "www.shell.us": true, "rivian.com": true}
	seen := map[string]bool{}
	for _, m := range membershipTerms {
		if _, ok := networkBySlug(m.Slug); !ok {
			t.Errorf("%s/%s: not a network in the table", m.Slug, m.Plan)
		}
		if m.Plan == "" || m.Terms == "" {
			t.Errorf("%s: a row needs a plan and its terms", m.Slug)
		}
		u, err := url.Parse(m.SourceURL)
		if err != nil || u.Scheme != "https" || !hosts[u.Host] || u.RawQuery != "" || u.Fragment != "" {
			t.Errorf("%s/%s: source %q is not a plain https page of the operator", m.Slug, m.Plan, m.SourceURL)
		}
		if _, err := time.Parse("2006-01-02", m.Checked); err != nil {
			t.Errorf("%s/%s: checked date %q", m.Slug, m.Plan, m.Checked)
		}
		if m.ReadVia != "page" && m.ReadVia != "search snippet" {
			t.Errorf("%s/%s: read via %q", m.Slug, m.Plan, m.ReadVia)
		}
		if k := m.Slug + "/" + m.Plan; seen[k] {
			t.Errorf("%s listed twice", k)
		} else {
			seen[k] = true
		}
		for _, bad := range []string{"overpaid", "wasted", "guaranteed", "cheapest", "best"} {
			if strings.Contains(strings.ToLower(m.Terms), bad) {
				t.Errorf("%s/%s: %q in the terms", m.Slug, m.Plan, bad)
			}
		}
	}
	// A network that states no percentage gets none: no unsourced numbers.
	for _, m := range membershipTerms {
		if m.Slug == "ran" && strings.ContainsAny(m.Terms, "%0123456789") {
			t.Errorf("the Rivian row carries a number the operator does not state: %q", m.Terms)
		}
		// Fees growth could not read on the operator's own page stay out.
		if (m.Slug == "ea" && m.Plan == "Pass+" || m.Slug == "tesla_sc") && strings.Contains(m.Terms, "$") {
			t.Errorf("%s/%s states a fee that was not read on the operator's page: %q", m.Slug, m.Plan, m.Terms)
		}
	}
}

func TestMembershipTermsHideAfterTwelveMonths(t *testing.T) {
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	rows := MembershipTermsAt(at(2026, 10, 21))
	if len(rows) != len(membershipTerms) {
		t.Fatalf("%d rows shown, want all %d", len(rows), len(membershipTerms))
	}
	r := rows[0]
	if r.Network == "" || r.Label != "As listed by "+r.Network+"; may be out of date; last checked 2026-10-20." {
		t.Errorf("label = %q", r.Label)
	}
	if n := len(MembershipTermsAt(at(2027, 10, 19))); n != len(membershipTerms) {
		t.Errorf("a day before 12 months: %d rows", n)
	}
	if n := len(MembershipTermsAt(at(2027, 10, 20))); n != 0 {
		t.Errorf("12 months after the check: %d rows still shown", n)
	}
}
