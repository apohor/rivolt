package onboardmail

import (
	"strings"
	"testing"
)

var testLinks = Links{App: "https://rivolt.dev/", Demo: "https://rivolt.dev/demo", Unsubscribe: "https://rivolt.dev/api/onboarding-email/unsubscribe?t=TOK"}

// Every email names the one step, carries the unsubscribe link, and has
// nothing that tracks: only the plain app, demo and unsubscribe URLs.
func TestEveryEmailHasUnsubscribeAndNoTracking(t *testing.T) {
	cases := map[string]Content{
		"welcome":          Welcome(testLinks),
		"nudge no vehicle": Nudge("no_vehicle", 1, testLinks),
		"nudge 2":          Nudge("no_vehicle", 2, testLinks),
		"nudge no data":    Nudge("no_data", 1, testLinks),
		"nudge no data 2":  Nudge("no_data", 2, testLinks),
	}
	for name, c := range cases {
		if c.Subject == "" || c.Key == "" || c.UnsubscribeURL != testLinks.Unsubscribe {
			t.Errorf("%s: incomplete %+v", name, c)
		}
		if !strings.Contains(c.Text, testLinks.Unsubscribe) || !strings.Contains(c.Text, "https://rivolt.dev/\n") {
			t.Errorf("%s: missing unsubscribe or app link:\n%s", name, c.Text)
		}
		for _, u := range allURLs(c.Text) {
			switch {
			case u == "https://rivolt.dev/", u == "https://rivolt.dev/demo", u == testLinks.Unsubscribe:
			default:
				t.Errorf("%s: unexpected URL %q", name, u)
			}
		}
		if strings.Contains(c.Text, "<img") || strings.Contains(c.Text, "utm_") {
			t.Errorf("%s: tracking markup", name)
		}
		if strings.Contains(strings.ToLower(c.Text), "password is") && !strings.Contains(c.Text, "never stored") {
			t.Errorf("%s: password sentence without the never-stored promise", name)
		}
	}
	if !strings.Contains(cases["welcome"].Text, testLinks.Demo) {
		t.Error("welcome has no demo link")
	}
	if !strings.Contains(cases["nudge no data"].Text, "Authorized Driver") {
		t.Error("no-data nudge does not explain the usual cause")
	}
}

func TestNudgeKeysDifferPerAttempt(t *testing.T) {
	if Nudge("no_vehicle", 1, testLinks).Key == Nudge("no_vehicle", 2, testLinks).Key {
		t.Error("attempts share an idempotency key")
	}
	if Welcome(testLinks).Key == Nudge("no_vehicle", 1, testLinks).Key {
		t.Error("welcome and nudge share a key")
	}
}

// Without a base URL the emails still read sensibly and carry no demo link.
func TestCopyWithoutBaseURL(t *testing.T) {
	c := Welcome(Links{Unsubscribe: "https://x/u"})
	if !strings.Contains(c.Text, "your Rivolt account") || strings.Contains(c.Text, "demo") {
		t.Errorf("text:\n%s", c.Text)
	}
}

func allURLs(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		if strings.HasPrefix(f, "https://") || strings.HasPrefix(f, "http://") {
			out = append(out, strings.TrimRight(f, ".,;)"))
		}
	}
	return out
}

// The welcome carries one plain iPhone-app line when the install has the link, and nothing
// else changes; the setup reminders never carry it.
func TestWelcomeIOSAppLine(t *testing.T) {
	const link = "https://apps.apple.com/us/app/rivolt/id1234567890"
	without := Welcome(testLinks)
	if strings.Contains(without.Text, "iPhone") {
		t.Errorf("no link configured, yet the welcome mentions the app:\n%s", without.Text)
	}
	l := testLinks
	l.IOSApp = link
	with := Welcome(l)
	if !strings.Contains(with.Text, "Rivolt is also an iPhone app: "+link+"\n") {
		t.Errorf("missing the app line:\n%s", with.Text)
	}
	if strings.Count(with.Text, "http") != strings.Count(without.Text, "http")+1 {
		t.Errorf("the line should add exactly one link")
	}
	if strings.Contains(with.Text, "?") && strings.Contains(strings.SplitN(with.Text, "iPhone app: ", 2)[1], "utm") {
		t.Error("tracking parameter in the app link")
	}
	for _, n := range []Content{Nudge("no_vehicle", 1, l), Nudge("no_data", 2, l)} {
		if strings.Contains(n.Text, "iPhone") {
			t.Errorf("a setup reminder carries the app line:\n%s", n.Text)
		}
	}
}
