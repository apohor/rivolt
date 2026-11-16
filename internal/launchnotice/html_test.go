package launchnotice

import (
	"html"
	"regexp"
	"strings"
	"testing"
)

var htmlTagRE = regexp.MustCompile(`(?s)<style>.*?</style>|<title>.*?</title>|<[^>]+>`)

// visibleWords is the words a reader sees in an HTML part, without the shell's mark and wordmark.
func visibleWords(h string) string {
	h = strings.NewReplacer("<br>", " ", "</p>", " ", "</td>", " ").Replace(h)
	w := strings.Fields(html.UnescapeString(htmlTagRE.ReplaceAllString(h, "")))
	if len(w) > 2 && w[0] == "ϟ" && w[1] == "Rivolt" {
		w = w[2:]
	}
	return strings.Join(w, " ")
}

func TestHTMLPartsSayExactlyTheTextWithTheDatesFilled(t *testing.T) {
	p := NewParams(day("2026-11-16"))
	cases := map[string][2]string{
		"launch notice":        {RenderEmail(p), RenderEmailHTML(p)},
		"founding, free":       {RenderFoundingEmail(FoundingParams{End: "2027-11-15"}), RenderFoundingEmailHTML(FoundingParams{End: "2027-11-15"})},
		"founding, subscribed": {RenderFoundingEmail(FoundingParams{End: "2027-11-15", Subscribed: true, NextCharge: "2027-11-15"}), RenderFoundingEmailHTML(FoundingParams{End: "2027-11-15", Subscribed: true, NextCharge: "2027-11-15"})},
		"founding, extended":   {RenderFoundingEmail(FoundingParams{End: "2027-11-15", ExtendedFrom: "2027-11-01"}), RenderFoundingEmailHTML(FoundingParams{End: "2027-11-15", ExtendedFrom: "2027-11-01"})},
		"renewal":              {RenderRenewalEmail(RenewalParams{RenewsOn: "2027-03-02", PriceLabel: "$34.99"}), RenderRenewalEmailHTML(RenewalParams{RenewsOn: "2027-03-02", PriceLabel: "$34.99"})},
	}
	for name, c := range cases {
		text, h := c[0], c[1]
		if got, want := visibleWords(h), strings.Join(strings.Fields(text), " "); got != want {
			t.Errorf("%s: the HTML says something else than the text\nhtml: %q\ntext: %q", name, got, want)
		}
		if tok := Unfilled(h); tok != "" {
			t.Errorf("%s: the HTML still has %q", name, tok)
		}
		for _, bad := range []string{"{{", "}}", "%!", "<nil>", "undefined"} {
			if strings.Contains(visibleWords(h), bad) {
				t.Errorf("%s: the visible HTML contains %q", name, bad)
			}
		}
	}
	h := RenderEmailHTML(p)
	for _, d := range []string{"November 16, 2026", "November 16, 2027", "December 16, 2026"} {
		if !strings.Contains(h, d) {
			t.Errorf("the notice HTML does not state %s", d)
		}
	}
	if !strings.Contains(h, `href="https://rivolt.dev/terms/2026-10-23"`) {
		t.Error("the previous Terms link is not a link in the HTML")
	}
}
