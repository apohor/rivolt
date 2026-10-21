package dcfcrates

import (
	"fmt"
	"strings"
	"time"
)

// Membership terms: what each network says its membership plans cost and give,
// each with the operator page it was read from and the date it was last checked.
// They are facts as the operator states them, copied by hand from the sources named
// in docs/DCFC_PRICING.md's recheck notes; never a price per kWh, never a percentage
// a network does not state itself, and never merged into a user's own rates.
//
// Rules (DA, 2026-10-20): every row says "as listed, may be out of date; last checked
// <date>"; a row last checked over 12 months ago is not shown; no row without an
// operator source.

// MembershipTerm is one plan of one network.
type MembershipTerm struct {
	Slug      string // the network's slug in Networks
	Plan      string
	Terms     string // as the operator states it
	SourceURL string // the operator page
	// ReadVia says how the text was read: "page" (the operator's page was fetched) or
	// "search snippet" (the page itself could not be fetched; the text is what the
	// operator's own page showed in search results).
	ReadVia string
	Checked string // YYYY-MM-DD
}

// membershipTerms are the rows, checked by hand on 2026-10-20 (growth's recheck).
// Networks with no operator source for a plan are absent. Fees growth could not
// read on the operator's own page (Electrify America Pass+, Tesla Supercharging
// Membership) are deliberately omitted rather than taken from a third party.
var membershipTerms = []MembershipTerm{
	{Slug: "ea", Plan: "Pass+", Terms: "Save about 25% on charging compared with guest pricing. Pricing is set per station and shown in the app.",
		SourceURL: "https://www.electrifyamerica.com/pricing/", ReadVia: "page", Checked: "2026-10-20"},
	{Slug: "ea", Plan: "Guest (no account)", Terms: "Station-specific pricing; some stations have time-of-use pricing.",
		SourceURL: "https://www.electrifyamerica.com/pricing/", ReadVia: "page", Checked: "2026-10-20"},
	{Slug: "tesla_sc", Plan: "Supercharging Membership (non-Tesla vehicles)", Terms: "Members pay the same per-kWh price as Tesla vehicles. A monthly fee is charged to the designated payment method. The price at each site is shown in the Tesla app.",
		SourceURL: "https://www.tesla.com/support/charging/supercharging-other-evs", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "evgo", Plan: "Pay As You Go", Terms: "No monthly fee; a $0.99 session fee.",
		SourceURL: "https://www.evgo.com/pricing/", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "evgo", Plan: "EVgo Plus", Terms: "$6.99 a month; up to 15% off standard rates; free reservations; no session fees.",
		SourceURL: "https://www.evgo.com/pricing/", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "evgo", Plan: "EVgo PlusMax", Terms: "$12.99 a month; up to 30% off standard rates; free reservations; no session fees.",
		SourceURL: "https://www.evgo.com/pricing/", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "shell_recharge", Plan: "Recharge Rewards Silver", Terms: "Free; 1¢ per kWh off at participating stations.",
		SourceURL: "https://www.shell.us/electric-vehicle-charging/shell-app-ev-faqs.html", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "shell_recharge", Plan: "Recharge Rewards Gold", Terms: "$6.95 a month; 5¢ per kWh off at participating stations.",
		SourceURL: "https://www.shell.us/electric-vehicle-charging/shell-app-ev-faqs.html", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "shell_recharge", Plan: "Recharge Rewards Platinum", Terms: "$13.95 a month; 10¢ per kWh off at participating stations.",
		SourceURL: "https://www.shell.us/electric-vehicle-charging/shell-app-ev-faqs.html", ReadVia: "search snippet", Checked: "2026-10-20"},
	{Slug: "ran", Plan: "Rivian vehicles", Terms: "Rivian drivers receive a discounted charging rate at sites open to all EVs, applied automatically. No percentage is stated. The price is shown in the Rivian app, on the vehicle display or at the charger.",
		SourceURL: "https://rivian.com/support/article/how-much-does-it-cost-to-charge-on-the-rivian-adventure-network", ReadVia: "search snippet", Checked: "2026-10-20"},
}

// membershipMaxAge is how many months after it was checked a row stops being shown.
const membershipMaxAge = 12

// ShownMembershipTerm is a row as the API gives it: the term, the network's display
// name, and the label every row carries.
type ShownMembershipTerm struct {
	MembershipTerm
	Network string
	Label   string
}

// MembershipTermsAt returns the rows that are still shown at now: checked less than
// 12 months ago. Each carries the label "As listed by <network>; may be out of date;
// last checked <date>.".
func MembershipTermsAt(now time.Time) []ShownMembershipTerm {
	var out []ShownMembershipTerm
	for _, t := range membershipTerms {
		checked, err := time.Parse("2006-01-02", t.Checked)
		if err != nil || !now.Before(checked.AddDate(0, membershipMaxAge, 0)) {
			continue
		}
		name := t.Slug
		if n, ok := networkBySlug(t.Slug); ok {
			name = n.DisplayName
		}
		out = append(out, ShownMembershipTerm{
			MembershipTerm: t, Network: name,
			Label: fmt.Sprintf("As listed by %s; may be out of date; last checked %s.", strings.TrimSuffix(name, "."), t.Checked),
		})
	}
	return out
}
