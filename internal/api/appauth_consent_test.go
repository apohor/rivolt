package api

import (
	"strings"
	"testing"
)

// Cloudflare's Email Address Obfuscation rewrites addresses in HTML to
// "[email protected]" unless they sit between email_off markers, and the
// consent page's CSP blocks its decode script. The page must carry the
// markers (html/template would drop them if written in the template) and
// still escape the name.
func TestConsentPageKeepsTheEmailVisible(t *testing.T) {
	for name, want := range map[string]string{
		"sam@example.com":      "<strong><!--email_off-->sam@example.com<!--/email_off--></strong>",
		"<b>x</b>@example.com": "<strong><!--email_off-->&lt;b&gt;x&lt;/b&gt;@example.com<!--/email_off--></strong>",
		"a--><script>@x.io":    "<strong><!--email_off-->a--&gt;&lt;script&gt;@x.io<!--/email_off--></strong>",
	} {
		var b strings.Builder
		if err := appConsentPage.Execute(&b, appConsent{Username: emailOff(name), Action: appFinishPath, Token: "t"}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), want) {
			t.Errorf("%q: page lacks %q:\n%s", name, want, b.String())
		}
	}
}

// The Cancel button had no text colour of its own: in the iOS sign-in sheet in dark appearance a button takes the system's light
// text, which on this page's white background is invisible. Every button states its colour in both appearances, and the page says
// it supports both so the sheet does not mix them.
func TestConsentPageButtonsAreReadableInLightAndDark(t *testing.T) {
	var b strings.Builder
	if err := appConsentPage.Execute(&b, appConsent{Username: emailOff("sam@example.com"), Action: appFinishPath, Token: "t"}); err != nil {
		t.Fatal(err)
	}
	page := b.String()
	for _, want := range []string{
		`<meta name="color-scheme" content="light dark">`,
		":root{color-scheme:light dark}",
		"border:1px solid #ccc;background:none;color:#111}", // Cancel in light: dark text on the light page
		"@media (prefers-color-scheme:dark){body{background:#111;color:#f2f2f2}button{color:#f2f2f2;border-color:#666}", // Cancel in dark: light text on the dark page
		"button[value=allow]{background:#0a7d32;color:#fff;",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("consent page lacks %q", want)
		}
	}
	if !strings.Contains(page, `value="deny">Cancel</button>`) {
		t.Error("the Cancel button is gone")
	}
}
