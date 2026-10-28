// Package statement builds and sends the monthly statement email
// (scratchpad/statement-email-spec.md): pure rendering from the
// ledger's own Monthly, the signed unsubscribe token, the preference
// and send-state store, and the idempotent hourly job.
package statement

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/apohor/rivolt/internal/iosapp"
	"github.com/apohor/rivolt/internal/ledger"
)

// Links are absolute URLs on the sender-aligned domain; src=statement
// is the only parameter added (no tracking pixel, no per-user ids).
type Links struct {
	Statement string
	Share     string
	Unpriced  string
	Settings  string
	// Unsubscribe is the signed one-click link (GET shows a page,
	// POST performs it).
	Unsubscribe string
	// IOSApp is the iPhone app link (RIVOLT_IOS_APP_URL); empty leaves the line out.
	IOSApp string
}

// Input is everything the template needs.
type Input struct {
	Period    string // YYYY-MM, the month being reported
	FirstName string
	Currency  string
	Month     ledger.Monthly
	// Prev is the previous month's statement, or nil when it could not
	// be read.
	Prev  *ledger.Monthly
	Links Links
	// Savings is the month's off-peak saving estimate, or nil. It is only ever
	// built for a user who is already getting this email.
	Savings *ledger.Savings
}

// Rendered is a ready-to-send message.
type Rendered struct {
	Subject string
	Text    string
	HTML    string
	// Silent is true when the month had no sessions and no drives:
	// nothing is sent.
	Silent bool
}

// Silent reports the no-email condition (spec §1.5).
func Silent(m ledger.Monthly) bool {
	return m.Energy.Sessions == 0 && m.Driving.Drives == 0 && len(m.Receipts) == 0
}

// notPricedYet is the lead-in when nothing in the month has a price.
func notPricedYet(s ledger.Spend) bool { return s.PricedSessions == 0 && s.UnpricedSessions > 0 }

// MonthName renders "September 2026" for a YYYY-MM period.
func MonthName(period string) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return period
	}
	return t.Format("January 2006")
}

// Money formats an amount in the statement currency.
func Money(v float64, currency string) string {
	neg := v < 0
	v = math.Abs(v)
	s := fmt.Sprintf("%.2f", v)
	switch strings.ToUpper(currency) {
	case "USD", "CAD", "AUD", "":
		s = "$" + s
	case "EUR":
		s = "€" + s
	case "GBP":
		s = "£" + s
	default:
		s = s + " " + strings.ToUpper(currency)
	}
	if neg {
		return "-" + s
	}
	return s
}

func kwh(v float64) string { return fmt.Sprintf("%.0f kWh", v) }

type splitLine struct {
	Label string
	Cost  float64
	KWh   float64
}

// splits mirrors the Money tab's Home / Away / Fast bar.
func splits(m ledger.Monthly) []splitLine {
	var out []splitLine
	if pk := m.Energy.ByPlaceKind; pk != nil {
		add := func(label string, l ledger.KindLine) {
			if l.Sessions > 0 || l.KWh > 0 || l.Cost > 0 {
				out = append(out, splitLine{label, l.Cost, l.KWh})
			}
		}
		add("Home", pk["home"]["ac"])
		add("Away", pk["away"]["ac"])
		add("Fast charging", m.Energy.ByKind["dcfc"])
		add("Other", pk["unknown"]["ac"])
		return out
	}
	add := func(label string, l ledger.KindLine) {
		if l.Sessions > 0 {
			out = append(out, splitLine{label, l.Cost, l.KWh})
		}
	}
	add("Charging (AC)", m.Energy.ByKind["ac"])
	add("Fast charging", m.Energy.ByKind["dcfc"])
	return out
}

type bigCharge struct {
	Date, Kind, Source string
	KWh, Cost          float64
	Priced             bool
}

func sourceLabel(src string) string {
	switch src {
	case ledger.SourceRivian:
		return "billed"
	case ledger.SourceNetworkEstimate, ledger.SourceObservedEstimate:
		return "estimated"
	case ledger.SourceUser:
		return "entered"
	case ledger.SourceHomeRate:
		return "at your home rate"
	}
	return ""
}

// biggest returns the top 3 priced receipts by cost. Place words only:
// "Home", "Away" or "Fast charge" plus a public network name; never
// coordinates or addresses.
func biggest(m ledger.Monthly) []bigCharge {
	var cs []bigCharge
	for _, r := range m.Receipts {
		if r.Cost == nil || r.CostSource == ledger.SourceUnknown || r.CostSource == ledger.SourceHomeRateMisapplied {
			continue
		}
		kind := "Away"
		switch {
		case r.Kind == "dcfc":
			kind = "Fast charge"
			if r.Network != nil && strings.TrimSpace(*r.Network) != "" {
				kind += " · " + strings.TrimSpace(*r.Network)
			}
		case r.Place == "home":
			kind = "Home"
		}
		cs = append(cs, bigCharge{Date: r.LocalDate, Kind: kind, KWh: r.KWh, Cost: *r.Cost,
			Source: sourceLabel(r.CostSource), Priced: true})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Cost > cs[j].Cost })
	if len(cs) > 3 {
		cs = cs[:3]
	}
	return cs
}

// savingsLine is the one cost-only sentence about moving home charging into the
// user's off-peak window, or "" when there is nothing worth saying (no figure,
// under a dollar, or under a kWh). No tips and no links.
func savingsLine(s *ledger.Savings, currency string) string {
	if s == nil || s.Needs != "" || s.CouldHaveSaved == nil || *s.CouldHaveSaved < 1 || s.MovedKWh < 1 {
		return ""
	}
	whole := Money(math.Round(*s.CouldHaveSaved), currency)
	whole = strings.TrimSuffix(whole, ".00")
	return fmt.Sprintf("Moving %.0f kWh into your off-peak window would have saved about %s (estimate).", s.MovedKWh, whole)
}

func comparable(a, b *ledger.Monthly) bool {
	return a != nil && b != nil && a.Spend.UnpricedSessions == 0 && b.Spend.UnpricedSessions == 0 &&
		a.Spend.PricedSessions > 0 && b.Spend.PricedSessions > 0
}

func deltaLine(cur ledger.Monthly, prev *ledger.Monthly, currency string) string {
	if !comparable(&cur, prev) {
		return ""
	}
	d := cur.Spend.Total - prev.Spend.Total
	if math.Abs(d) < 0.005 {
		return "About the same as " + MonthName(prev.Period)
	}
	word := "more"
	if d < 0 {
		word = "less"
	}
	return fmt.Sprintf("%s %s than %s", Money(math.Abs(d), currency), word, strings.Fields(MonthName(prev.Period))[0])
}

// Render builds subject, text and HTML. Totals come straight from the
// ledger's Monthly: nothing is re-summed except the display split of
// numbers the ledger already provides.
func Render(in Input) Rendered {
	m := in.Month
	cur := in.Currency
	if cur == "" {
		cur = m.Currency
	}
	if Silent(m) {
		return Rendered{Silent: true}
	}
	name := MonthName(in.Period)
	month := strings.Fields(name)[0]
	np := notPricedYet(m.Spend)

	var subject string
	if np {
		subject = fmt.Sprintf("Your Rivian charging for %s: not priced yet", month)
	} else {
		subject = fmt.Sprintf("Your Rivian cost for %s: %s", month, Money(m.Spend.Total, cur))
	}

	hello := "Hi there,"
	if fn := strings.TrimSpace(in.FirstName); fn != "" {
		hello = "Hi " + fn + ","
	}

	var heading string
	if np {
		heading = "Charging for " + month + ": not priced yet"
	} else {
		heading = "You spent " + Money(m.Spend.Total, cur) + " on charging"
	}
	facts := fmt.Sprintf("%s · %.0f mi", kwh(m.Energy.KWhAdded), m.Driving.Miles)
	if m.Driving.CostPerMile != nil && !np {
		facts += fmt.Sprintf(" · %s/mi", Money(*m.Driving.CostPerMile, cur))
	}
	delta := deltaLine(m, in.Prev, cur)
	saving := savingsLine(in.Savings, cur)

	sp := splits(m)
	big := biggest(m)

	var unpriced []string
	if m.Spend.UnpricedSessions > 0 {
		s := fmt.Sprintf("Not priced yet: %d charge%s, %s. They aren't in the total above.",
			m.Spend.UnpricedSessions, plural(m.Spend.UnpricedSessions), kwh(m.Spend.UnpricedKWh))
		unpriced = append(unpriced, s)
		if m.Spend.UnpricedHomeRateEstimate != nil {
			unpriced = append(unpriced, fmt.Sprintf("About %s at your home rate.", Money(*m.Spend.UnpricedHomeRateEstimate, cur)))
		}
	}
	var quality []string
	if m.Spend.EstimatedShare != nil && *m.Spend.EstimatedShare > 0 {
		quality = append(quality, "Some amounts are estimated from public network rates, not billed.")
	}
	if m.Energy.IncludesEstimated {
		quality = append(quality, "Some charges this month were estimated from the battery's change, because a Rivolt bug lost their energy count; their kWh and cost are estimates.")
	}
	if m.DataQuality.CurrencyMismatchExcluded > 0 {
		quality = append(quality, fmt.Sprintf("%d charge%s used another currency and %s left out.",
			m.DataQuality.CurrencyMismatchExcluded, plural(m.DataQuality.CurrencyMismatchExcluded),
			map[bool]string{true: "was", false: "were"}[m.DataQuality.CurrencyMismatchExcluded == 1]))
	}
	if n := m.DataQuality.SupersededExcluded + m.DataQuality.DrivesSupersededExcluded; n > 0 {
		quality = append(quality, fmt.Sprintf("We left out %d duplicate record%s.", n, plural(n)))
	}

	const foot = "You're getting this because you turned on monthly statements in Rivolt."
	const indep = "Rivolt is an independent, open-source app and is not affiliated with Rivian."

	// ---- text
	var t strings.Builder
	fmt.Fprintf(&t, "%s\n\nHere's your %s statement from Rivolt.\n\n%s\n  %s\n", hello, name, heading, facts)
	if delta != "" {
		fmt.Fprintf(&t, "  %s\n", delta)
	}
	if len(sp) > 0 {
		t.WriteString("\nWhere it went\n")
		for _, s := range sp {
			fmt.Fprintf(&t, "  %-14s %10s  %s\n", s.Label, Money(s.Cost, cur), kwh(s.KWh))
		}
	}
	if saving != "" {
		t.WriteString("\n" + saving + "\n")
	}
	if len(big) > 0 {
		t.WriteString("\nBiggest charges\n")
		for _, b := range big {
			src := ""
			if b.Source != "" {
				src = " (" + b.Source + ")"
			}
			fmt.Fprintf(&t, "  %s  %s  %s  %s%s\n", b.Date, b.Kind, kwh(b.KWh), Money(b.Cost, cur), src)
		}
	}
	if len(unpriced) > 0 {
		t.WriteString("\n" + strings.Join(unpriced, "\n") + "\n")
		if in.Links.Unpriced != "" {
			t.WriteString("Price them: " + in.Links.Unpriced + "\n")
		}
	}
	if len(quality) > 0 {
		t.WriteString("\n" + strings.Join(quality, "\n") + "\n")
	}
	fmt.Fprintf(&t, "\nSee the full statement: %s\n", in.Links.Statement)
	if in.Links.Share != "" {
		fmt.Fprintf(&t, "Share it: %s\n", in.Links.Share)
	}
	if in.Links.IOSApp != "" {
		fmt.Fprintf(&t, "\n%s%s\n", iosapp.Line, in.Links.IOSApp)
	}
	fmt.Fprintf(&t, "\n%s\nChange or stop it: %s   Unsubscribe: %s\n%s\n", foot, in.Links.Settings, in.Links.Unsubscribe, indep)

	// ---- html (single column, system fonts, no images, no remote content)
	e := html.EscapeString
	var h strings.Builder
	h.WriteString(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><meta name="supported-color-schemes" content="light dark"><title>` + e(subject) + `</title></head><body style="margin:0;padding:24px;background:#f6f7f8;color:#111;font:16px/1.5 -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif"><div style="max-width:560px;margin:0 auto;background:#fff;border-radius:10px;padding:24px">`)
	fmt.Fprintf(&h, `<p style="margin:0 0 16px">%s</p><p style="margin:0 0 16px">Here's your %s statement from Rivolt.</p>`, e(hello), e(name))
	fmt.Fprintf(&h, `<h1 style="font-size:22px;margin:0 0 4px">%s</h1><p style="margin:0 0 4px;color:#555">%s</p>`, e(heading), e(facts))
	if delta != "" {
		fmt.Fprintf(&h, `<p style="margin:0 0 16px;color:#555">%s</p>`, e(delta))
	}
	if len(sp) > 0 {
		h.WriteString(`<h2 style="font-size:16px;margin:20px 0 6px">Where it went</h2><table role="presentation" style="width:100%;border-collapse:collapse">`)
		for _, s := range sp {
			fmt.Fprintf(&h, `<tr><td style="padding:3px 0">%s</td><td style="padding:3px 0;text-align:right">%s</td><td style="padding:3px 0 3px 12px;text-align:right;color:#555">%s</td></tr>`, e(s.Label), e(Money(s.Cost, cur)), e(kwh(s.KWh)))
		}
		h.WriteString(`</table>`)
	}
	if saving != "" {
		fmt.Fprintf(&h, `<p style="margin:16px 0 0;color:#555">%s</p>`, e(saving))
	}
	if len(big) > 0 {
		h.WriteString(`<h2 style="font-size:16px;margin:20px 0 6px">Biggest charges</h2><table role="presentation" style="width:100%;border-collapse:collapse">`)
		for _, b := range big {
			src := ""
			if b.Source != "" {
				src = " (" + b.Source + ")"
			}
			fmt.Fprintf(&h, `<tr><td style="padding:3px 0">%s · %s</td><td style="padding:3px 0;text-align:right;color:#555">%s</td><td style="padding:3px 0 3px 12px;text-align:right">%s%s</td></tr>`, e(b.Date), e(b.Kind), e(kwh(b.KWh)), e(Money(b.Cost, cur)), e(src))
		}
		h.WriteString(`</table>`)
	}
	if len(unpriced) > 0 {
		h.WriteString(`<p style="margin:20px 0 4px"><strong>` + e(strings.Join(unpriced, " ")) + `</strong>`)
		if in.Links.Unpriced != "" {
			h.WriteString(` <a href="` + e(in.Links.Unpriced) + `">Price them</a>`)
		}
		h.WriteString(`</p>`)
	}
	for _, q := range quality {
		fmt.Fprintf(&h, `<p style="margin:8px 0;color:#555">%s</p>`, e(q))
	}
	fmt.Fprintf(&h, `<p style="margin:20px 0 4px"><a href="%s">See the full statement</a>`, e(in.Links.Statement))
	if in.Links.Share != "" {
		fmt.Fprintf(&h, ` · <a href="%s">Share it</a>`, e(in.Links.Share))
	}
	if in.Links.IOSApp != "" {
		fmt.Fprintf(&h, `</p><p style="margin:12px 0 0;color:#555">%s<a href="%s">%s</a>`, e(strings.TrimSpace(iosapp.Line)+" "), e(in.Links.IOSApp), e(in.Links.IOSApp))
	}
	fmt.Fprintf(&h, `</p><hr style="border:0;border-top:1px solid #ddd;margin:20px 0"><p style="margin:0 0 6px;color:#666;font-size:13px">%s</p><p style="margin:0 0 6px;color:#666;font-size:13px"><a href="%s">Change or stop it</a> · <a href="%s">Unsubscribe</a></p><p style="margin:0;color:#666;font-size:13px">%s</p></div></body></html>`,
		e(foot), e(in.Links.Settings), e(in.Links.Unsubscribe), e(indep))

	return Rendered{Subject: subject, Text: t.String(), HTML: h.String()}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Period returns the YYYY-MM of the month before the one containing t,
// in loc.
func PreviousMonth(t time.Time, loc *time.Location) string {
	l := t.In(loc)
	first := time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
	return first.AddDate(0, -1, 0).Format("2006-01")
}
