package api

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestDemoVisitSource(t *testing.T) {
	cases := []struct{ utm, host, want string }{
		{"reddit", "", "reddit"},
		{"x", "", "x"},
		{"twitter", "", "x"},
		{"rivianforums", "", "rivianforums"},
		{"youtube", "", "youtube"},
		{"github", "", "github"},
		{"test1", "", "other"},
		{"test1", "www.reddit.com", "other"}, // utm wins, even when unlisted
		{"reddit", "news.example", "reddit"},
		{"", "www.reddit.com", "reddit"},
		{"", "old.reddit.com", "reddit"},
		{"", "t.co", "x"},
		{"", "x.com", "x"},
		{"", "rivianforums.com", "rivianforums"},
		{"", "www.youtube.com", "youtube"},
		{"", "notreddit.com", "other"},
		{"", "reddit.com.evil.example", "other"},
		{"", "news.example", "other"},
		{"", "", "none"},
	}
	for _, c := range cases {
		if got := demoVisitSource(c.utm, c.host); got != c.want {
			t.Errorf("demoVisitSource(%q, %q) = %q, want %q", c.utm, c.host, got, c.want)
		}
	}
}

func demoVisits(t *testing.T, c *DemoVisitCounter, src string) float64 {
	t.Helper()
	return testutil.ToFloat64(c.vec.WithLabelValues(src))
}

func TestHandleDemoVisits(t *testing.T) {
	c := NewDemoVisitCounter()
	h := handleDemoVisits(c)
	post := func(body string, hdr map[string]string) int {
		return postEvents(h, body, hdr).Code
	}
	ev := func(name, props string) string {
		return `{"anon_id":"` + anon + `","events":[{"name":"` + name + `","props":` + props + `}]}`
	}
	if code := post(ev("demo_view", `{"utm_source":"reddit","referrer_host":"x.test"}`), nil); code != 204 {
		t.Fatalf("code %d", code)
	}
	post(ev("demo_view", `{"utm_source":"zz9"}`), nil)
	post(ev("demo_view", `{}`), nil)
	if demoVisits(t, c, "reddit") != 1 || demoVisits(t, c, "other") != 1 || demoVisits(t, c, "none") != 1 {
		t.Errorf("counts reddit=%v other=%v none=%v", demoVisits(t, c, "reddit"), demoVisits(t, c, "other"), demoVisits(t, c, "none"))
	}
	// Other events are dropped unread, not rejected, and never counted.
	if code := post(ev("receipt_open", `{"source":"billed"}`), nil); code != 204 {
		t.Errorf("other event: code %d", code)
	}
	// Do Not Track and Global Privacy Control: nothing counted.
	for _, h := range []map[string]string{{"DNT": "1"}, {"Sec-GPC": "1"}} {
		post(ev("demo_view", `{"utm_source":"x"}`), h)
	}
	if demoVisits(t, c, "x") != 0 {
		t.Errorf("counted under DNT/GPC")
	}
	for name, body := range map[string]string{
		"bad json": `{`,
		"bad anon": `{"anon_id":"short","events":[{"name":"demo_view"}]}`,
		"empty":    `{"anon_id":"` + anon + `","events":[]}`,
	} {
		if code := post(body, nil); code != 400 {
			t.Errorf("%s: code %d, want 400", name, code)
		}
	}
	// A hostile utm_source cannot mint a label: the series set is fixed.
	post(ev("demo_view", `{"utm_source":"`+strings.Repeat("a", 39)+`"}`), nil)
	if n := testutil.CollectAndCount(c.vec); n != len(demoVisitSources) {
		t.Errorf("series = %d, want %d", n, len(demoVisitSources))
	}
}
