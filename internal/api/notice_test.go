package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The transfer notice shows from the email's day until the transfer day, with the
// approved wording and the day spelled out; nothing outside that window or unconfigured.
func TestTransferNotice(t *testing.T) {
	if n, err := ParseTransferNotice("", ""); err != nil || n.Active(time.Now()) != nil {
		t.Fatalf("unconfigured = %+v %v, want no notice", n, err)
	}
	for _, bad := range [][2]string{{"2026-11-01", ""}, {"", "2026-12-01"}, {"2026-12-01", "2026-11-01"}, {"2026-11-01", "2026-11-01"}, {"Nov 1", "2026-12-01"}} {
		if _, err := ParseTransferNotice(bad[0], bad[1]); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	n, err := ParseTransferNotice("2026-11-01", " 2026-12-01 ")
	if err != nil {
		t.Fatal(err)
	}
	at := func(s string) time.Time {
		v, _ := time.Parse(time.RFC3339, s)
		return v
	}
	if n.Active(at("2026-10-31T23:59:59Z")) != nil || n.Active(at("2026-12-01T00:00:00Z")) != nil {
		t.Error("shown outside [from, date)")
	}
	got := n.Active(at("2026-11-01T00:00:00Z"))
	if got == nil {
		t.Fatal("not shown on the email's day")
	}
	want := "From December 1, 2026, Rivolt will be run by ZeroDowntime LLC. Nothing changes for your data; details in the email we sent, or export or delete your data in Settings → Data."
	if got.Text != want || got.ID != "transfer-2026-12-01" || got.StartsAt != "2026-11-01T00:00:00Z" || got.Until != "2026-12-01T00:00:00Z" ||
		got.LinkURL != "/settings?tab=data" {
		t.Errorf("notice = %+v", got)
	}
	b, _ := json.Marshal(map[string]any{"notice": n.Active(at("2027-01-01T00:00:00Z"))})
	if string(b) != `{"notice":null}` {
		t.Errorf("no notice encodes as %s, want null", b)
	}
	b, _ = json.Marshal(got)
	for _, k := range []string{`"id"`, `"text"`, `"starts_at"`, `"until"`, `"link_text"`, `"link_url"`} {
		if !strings.Contains(string(b), k) {
			t.Errorf("notice JSON lacks %s: %s", k, b)
		}
	}
}
