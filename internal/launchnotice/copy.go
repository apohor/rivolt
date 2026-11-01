// Package launchnotice is the Terms section 11 notice that Rivolt now has a free tier and Pro (Rivolt Pro, Sprint 14, item 10): its
// words, the dates it states and the per-account record that it was shown and emailed.
//
// The words are growth's (docs/PRO_LAUNCH_COPY.md, sections 1 and 2), rendered here from three dates so the email and the in-app notice
// can never disagree. Dates are fixed when an account's row is created (Params): a retried email states the dates the first attempt would
// have, so the stated notice period is never shorter than 30 days from the email.
package launchnotice

import (
	"fmt"
	"strings"
	"time"
)

// Kind of the launch notice.
const Kind = "launch"

// Params are the dates a notice states, as YYYY-MM-DD.
type Params struct {
	Launch         string `json:"launch"`          // LAUNCH_DATE: the hosted service has a free tier and Pro from here
	FoundingEnd    string `json:"founding_end"`    // FOUNDING_END_DATE: launch + 1 year, when the free Pro year ends
	TermsEffective string `json:"terms_effective"` // TERMS_EFFECTIVE_EXISTING: launch + 30 days, when the new Terms bind existing accounts
}

// NewParams derives the three dates from the launch date, so they cannot disagree.
func NewParams(launch time.Time) Params {
	l := launch.UTC()
	return Params{
		Launch:         l.Format("2006-01-02"),
		FoundingEnd:    l.AddDate(1, 0, 0).Format("2006-01-02"),
		TermsEffective: l.AddDate(0, 0, 30).Format("2006-01-02"),
	}
}

// ShowsUntil is the end of the day the new Terms take effect: the in-app notice is shown until then.
func (p Params) ShowsUntil() (time.Time, error) {
	t, err := time.Parse("2006-01-02", p.TermsEffective)
	if err != nil {
		return time.Time{}, err
	}
	return t.Add(24 * time.Hour), nil
}

func long(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	return t.Format("January 2, 2006")
}

// Link is a button of the in-app notice.
type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// InApp is the in-app notice: a title, a body and links.
type InApp struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Links []Link `json:"links"`
}

// RenderInApp is the banner on Now and the row in Settings (copy section 2).
func RenderInApp(p Params) InApp {
	return InApp{
		Title: "Rivolt now has a free tier and Pro",
		Body: "Your account has Pro at no charge until " + long(p.FoundingEnd) + ", with no card and nothing charged. The updated Terms take effect for you on " +
			long(p.TermsEffective) + ". Nothing is deleted when a plan ends, and the full export is always free; on the free plan one vehicle is shown at a time, all are recorded.",
		Links: []Link{
			{Label: "What changes", Href: "/support#free-and-pro"},
			{Label: "Read the Terms", Href: "/terms"},
		},
	}
}

// Subject of the email.
const Subject = "Rivolt Pro, and what it means for your account"

// RenderEmail is the plain-text email (copy section 1). It is a service notice: no unsubscribe link, the standard footer.
func RenderEmail(p Params) string {
	r := strings.NewReplacer(
		"LAUNCH_DATE", long(p.Launch),
		"FOUNDING_END_DATE", long(p.FoundingEnd),
		"TERMS_EFFECTIVE_EXISTING", long(p.TermsEffective),
	)
	return r.Replace(emailTemplate)
}

// Unfilled reports a placeholder left in rendered text.
func Unfilled(s string) string {
	for _, tok := range []string{"LAUNCH_DATE", "FOUNDING_END_DATE", "TERMS_EFFECTIVE_EXISTING", "[DECIDE", "TODO", "TBD", "XXX"} {
		if strings.Contains(s, tok) {
			return tok
		}
	}
	return ""
}

// Check refuses params that would state nonsense: a malformed date, or a Terms date fewer than 30 days after the launch date.
func (p Params) Check() error {
	l, err := time.Parse("2006-01-02", p.Launch)
	if err != nil {
		return fmt.Errorf("launch date: %w", err)
	}
	te, err := time.Parse("2006-01-02", p.TermsEffective)
	if err != nil {
		return fmt.Errorf("terms-effective date: %w", err)
	}
	if _, err := time.Parse("2006-01-02", p.FoundingEnd); err != nil {
		return fmt.Errorf("founding-end date: %w", err)
	}
	if te.Sub(l) < 30*24*time.Hour {
		return fmt.Errorf("the new Terms take effect %d days after launch; Terms section 11 needs 30", int(te.Sub(l).Hours()/24))
	}
	return nil
}

const emailTemplate = `Hi,

Rivolt has been free during its public beta. From LAUNCH_DATE the hosted service at rivolt.dev has a free tier and a paid plan, Rivolt Pro.

Your account has Pro at no charge until FOUNDING_END_DATE. There is no card on file and nothing will be charged. We will email you and show a notice in the app at least 30 days before that date. After it, your account is on the free tier unless you choose to subscribe.

What the free tier keeps: Now, Activity, Money and Truck for the last 90 days; the monthly statement; a single-day trip planner; three saved trips; share cards; imports; every alert; and the full export of everything Rivolt holds, always, including history older than 90 days. Nothing is ever deleted when a plan ends.

If you have more than one vehicle: on the free plan, Rivolt keeps recording every drive and charge on all your vehicles, but shows one vehicle at a time (you choose which). Every vehicle stays in your full export, and Pro shows them all.

What Pro is: unlimited history and trends, range near a temperature, multi-day trip plans with hotel and campground overnights, re-plan from where the truck is, plan-vs-actual, unlimited saved trips, every vehicle shown, and the Lock Screen widgets and charging Live Activity on iPhone. $3.99 a month or $34.99 a year, the same on the web and in the app.

The Terms and the Privacy Policy change with this. The updated Terms (section 5, free tier and Pro, and the refund line in section 2) take effect for your account on TERMS_EFFECTIVE_EXISTING, 30 days from this email: https://rivolt.dev/terms. The Privacy Policy gains an "If you buy Rivolt Pro" section that applies only to accounts that subscribe: https://rivolt.dev/privacy. Until TERMS_EFFECTIVE_EXISTING the previous Terms apply: https://rivolt.dev/terms/2026-10-23. If you do not agree, you can export and delete your account from Settings before then.

Nothing to do today. Questions: support@rivolt.dev.

The Rivolt team
Rivolt is unofficial and not affiliated with Rivian.
`
