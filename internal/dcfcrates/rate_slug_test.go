package dcfcrates

import "testing"

func TestResolveRateBySlug(t *testing.T) {
	n := Networks[0]
	rr, ok := ResolveRateBySlug(n.Slug, nil)
	// A known network with no user rate is identified but NOT priced: the table's
	// dated guest rate is never returned as a price.
	if !ok || rr.Slug != n.Slug || rr.UserRate != 0 || rr.GuestRate != 0 || rr.AllMemberRate != 0 || rr.DisplayName != n.DisplayName {
		t.Fatalf("builtin: %+v ok=%v", rr, ok)
	}
	ov := []NetworkOverride{{Slug: n.Slug, GuestRate: 0.55, MemberRate: 0.40, MemberActive: true}}
	rr, ok = ResolveRateBySlug(n.Slug, ov)
	if !ok || rr.UserRate != 0.40 || rr.DisplayName != n.DisplayName {
		t.Fatalf("member override: %+v ok=%v", rr, ok)
	}
	ov[0].MemberActive = false
	if rr, _ = ResolveRateBySlug(n.Slug, ov); rr.UserRate != 0.55 {
		t.Fatalf("guest override: %+v", rr)
	}
	if _, ok := ResolveRateBySlug("no-such-network", nil); ok {
		t.Fatal("unknown slug resolved")
	}
	if rr, ok := ResolveRateBySlug("custom", []NetworkOverride{{Slug: "custom", Name: "My Net", GuestRate: 0.3}}); !ok || rr.DisplayName != "My Net" || rr.UserRate != 0.3 {
		t.Fatalf("custom: %+v ok=%v", rr, ok)
	}
}
