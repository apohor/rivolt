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
