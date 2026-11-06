package dcfcrates

import "testing"

// Rivian ends a charger name with the network's code in brackets. A network outside the built-in table was shown as "Unknown DCFC"; it is
// now named from the code, with a stable slug and no rate. Synthetic names only.
func TestACodeNamesANetworkTheTableDoesNotKnow(t *testing.T) {
	for _, c := range []struct{ name, display, slug string }{
		{"Test Plaza Alpha [RED_E]", "Red E", "code_red_e"},
		{"Sample Stop [red_e]", "Red E", "code_red_e"}, // mixed case
		{"Fake Lot [Red-E]", "Red E", "code_red_e"},    // hyphen read as underscore
		{"Fake Lot [ELECTRIC_ERA]", "Electric Era", "code_electric_era"},
		{"Fake Lot [FRANCIS]", "Francis Energy", "code_francis"},
		{"Fake Lot [bp_pulse]", "bp pulse", "code_bp_pulse"},
		{"Fake Lot [MERCEDES_BENZ]", "Mercedes-Benz", "code_mercedes_benz"},
		{"Fake Lot [CIRCLE_K]", "Circle K", "code_circle_k"},
		{"Fake Lot [ZZ_NEW_NET]", "Zz New Net", "code_zz_new_net"}, // not in the display map: title-cased
		{"Fake Lot [SOLO]", "Solo", "code_solo"},
		{"  Padded Name [ NEW_ONE ]  ", "New One", "code_new_one"},
	} {
		rr := ResolveRate(c.name, nil)
		sn := ResolveStop(c.name, nil)
		if rr.DisplayName != c.display || rr.Slug != c.slug || sn.DisplayName != c.display || sn.Slug != c.slug {
			t.Errorf("%q: rate %q/%q, stop %q/%q; want %q/%q", c.name, rr.DisplayName, rr.Slug, sn.DisplayName, sn.Slug, c.display, c.slug)
		}
		// Unpriced until the user sets a rate, like any network outside the table.
		if rr.GuestRate != 0 || rr.UserRate != 0 || rr.AllMemberRate != 0 || sn.UserSet || sn.UserPerKWh != 0 {
			t.Errorf("%q: a code network came with a price: %+v %+v", c.name, rr, sn)
		}
	}
}

// "Unknown DCFC" stays only when there is no code at all, and a name the table knows is unchanged whatever its bracket says.
func TestNoCodeStaysUnknownAndTheTableStillWins(t *testing.T) {
	for _, name := range []string{"No Bracket Fast Charger", "", "Odd [] Name", "Odd [ ]", "Bracket in the middle [RED_E] of a name", "Bad code [!!!]", "Too long [ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ABCDE]"} {
		if rr := ResolveRate(name, nil); rr.Slug != UnmatchedNetwork.Slug || rr.DisplayName != "Unknown DCFC" {
			t.Errorf("%q: got %q/%q, want Unknown DCFC", name, rr.DisplayName, rr.Slug)
		}
	}
	// The existing vectors: the table matches by name first, so a bracket with another code does not override it.
	for name, slug := range map[string]string{
		"Electrify America - Fake Plaza": "ea", "Fake Plaza Tesla Supercharger": "tesla_sc", "Fake Hotel [EVGO]": "evgo",
		"Electrify America Fake Lot [RED_E]": "ea",
	} {
		if got := ResolveStop(name, nil).Slug; got != slug {
			t.Errorf("%q: slug %q, want %q", name, got, slug)
		}
	}
	// MatchNetwork itself is unchanged: callers that ask whether the TABLE knows a network still get "unmatched".
	if n := MatchNetwork("Test Plaza Alpha [RED_E]"); n.Slug != UnmatchedNetwork.Slug {
		t.Errorf("MatchNetwork changed its answer for a code: %q", n.Slug)
	}
}

// A user's own row wins over the code's network, whether it is a slugged built-in row or a custom row named like the network.
func TestAUserRowStillWinsOverTheCodeNetwork(t *testing.T) {
	custom := []NetworkOverride{{Name: "Red E", GuestRate: 0.55}}
	rr := ResolveRate("Test Plaza Alpha [RED_E]", custom)
	if rr.GuestRate != 0.55 || rr.UserRate != 0.55 || rr.DisplayName != "Red E" {
		t.Errorf("custom row lost to the code network: %+v", rr)
	}
	sn := ResolveStop("Test Plaza Alpha [RED_E]", custom)
	if !sn.UserSet || sn.UserPerKWh != 0.55 || sn.DisplayName != "Red E" {
		t.Errorf("custom row lost in ResolveStop: %+v", sn)
	}
	// A custom row for another network does not touch it; a row with no rate is ignored as before.
	other := []NetworkOverride{{Name: "Francis Energy", GuestRate: 0.4}, {Name: "Red E", GuestRate: 0}}
	if rr := ResolveRate("Test Plaza Alpha [RED_E]", other); rr.Slug != "code_red_e" || rr.UserRate != 0 {
		t.Errorf("another network's row changed the answer: %+v", rr)
	}
}
