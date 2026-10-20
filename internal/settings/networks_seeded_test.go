package settings

import (
	"testing"

	"github.com/apohor/rivolt/internal/dcfcrates"
)

// The table's mid-2025 rates are dated estimates, not the user's price: the seeded book
// has none, and a stored book whose rows still hold them reads as unset.
func TestSeededBookHasNoRates(t *testing.T) {
	rows := DefaultChargingNetworks()
	if len(rows) == 0 {
		t.Fatal("no default networks")
	}
	for _, r := range rows {
		if r.PricePerKWh != 0 || r.MemberPrice != 0 || !r.Seeded || r.Slug == "" {
			t.Errorf("%s: seeded row %+v, want no rates and seeded", r.Name, r)
		}
	}
}

func TestMarkSeededTreatsUntouchedTableRowsAsUnset(t *testing.T) {
	ea := dcfcrates.Networks[0]
	member := 0.0
	if ea.MemberRate != nil {
		member = *ea.MemberRate
	}
	stored := []ChargingNetwork{
		// saved while the table's numbers were the seed: untouched
		{Name: ea.DisplayName, Slug: ea.Slug, PricePerKWh: ea.GuestRate, MemberPrice: member, MemberActive: true, MemberPlan: ea.MemberPlan, Currency: "USD"},
		// the user typed their own guest rate
		{Name: "EVgo", Slug: "evgo", PricePerKWh: 0.51, Currency: "USD"},
		// the user changed only the member rate
		{Name: "Tesla Supercharger", Slug: "tesla_sc", PricePerKWh: dcfcrates.Networks[1].GuestRate, MemberPrice: 0.33, Currency: "USD"},
		// a custom row: every number is theirs
		{Name: "My Local Network", PricePerKWh: 0.30, Currency: "USD"},
	}
	got := markSeeded(normalizeNetworks(stored))
	by := map[string]ChargingNetwork{}
	for _, r := range got {
		by[r.Name] = r
	}
	if r := by[ea.DisplayName]; !r.Seeded || r.PricePerKWh != 0 || r.MemberPrice != 0 || !r.MemberActive {
		t.Errorf("untouched row = %+v, want unset (prices 0, seeded) with the membership toggle kept", r)
	}
	if r := by["EVgo"]; r.Seeded || r.PricePerKWh != 0.51 {
		t.Errorf("typed row = %+v, want the user's 0.51 kept", r)
	}
	if r := by["Tesla Supercharger"]; r.Seeded || r.MemberPrice != 0.33 || r.PricePerKWh == 0 {
		t.Errorf("member-edited row = %+v, want it treated as the user's", r)
	}
	if r := by["My Local Network"]; r.Seeded || r.PricePerKWh != 0.30 {
		t.Errorf("custom row = %+v", r)
	}
}

// Saving keeps a built-in network's blank row (no rate entered yet) but still drops a
// half-filled custom row, so the book does not lose the networks the user has not priced.
func TestNormalizeKeepsBlankBuiltinRowsAndDropsBlankCustomOnes(t *testing.T) {
	got := normalizeNetworks([]ChargingNetwork{
		{Name: "Electrify America", Slug: "ea"},
		{Name: "Half Filled Custom"},
		{Name: "", Slug: "evgo", PricePerKWh: 0.4},
	})
	if len(got) != 1 || got[0].Slug != "ea" || got[0].PricePerKWh != 0 {
		t.Errorf("normalized = %+v, want only the blank built-in row", got)
	}
}
