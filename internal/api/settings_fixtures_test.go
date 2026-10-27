package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/apohor/rivolt/internal/offpeak"
	"github.com/apohor/rivolt/internal/settings"
)

// The iPhone's Settings → Charging fixtures (off-peak schedule, price book, membership terms, planner defaults, and a field error)
// are the server's own answers, marshaled from its own types, so a changed type fails here until the files are regenerated:
// RIVOLT_WRITE_FIXTURES=1 go test ./internal/api -run TestSettingsFixtures. The values are invented.
func settingsFixtures(t *testing.T) map[string]any {
	t.Helper()
	sched := offpeak.Schedule{TZ: "America/Los_Angeles", Windows: []offpeak.Window{
		{Days: "all", Start: "21:00", End: "07:00", Rate: 0.12, Label: "Overnight"},
		{Days: "weekends", Start: "10:00", End: "15:00", Rate: 0.09},
	}}
	bad := sched
	bad.Windows = []offpeak.Window{{Days: "all", Start: "21:00", End: "07:00", Rate: 0.12}, {Days: "all", Start: "25:00", End: "07:00", Rate: 0.12}}
	_, ferr := offpeak.Normalize(bad)
	if ferr == nil {
		t.Fatal("the invalid schedule was accepted")
	}
	lat, lon := 47.6205, -122.3493
	nets := []settings.ChargingNetwork{
		{Name: "Example Fast Network", PricePerKWh: 0.48, Currency: "USD", MemberPrice: 0.38, MemberActive: true, MemberPlan: "Example Pass", Slug: "example", Preferred: true},
		{Name: "Another Network", PricePerKWh: 0, Currency: "USD", Seeded: true},
		{Name: "My building's charger", PricePerKWh: 0.31, Currency: "USD", Lat: &lat, Lon: &lon},
	}
	terms := make([]membershipTermJSON, 0, 2)
	for _, r := range []struct{ slug, network, plan string }{{"example", "Example Fast Network", "Example Pass"}, {"another", "Another Network", "Another Plus"}} {
		terms = append(terms, membershipTermJSON{
			NetworkSlug: r.slug, Network: r.network, Plan: r.plan, Terms: "Example monthly fee for lower per-kWh rates.",
			SourceURL: "https://example.test/membership", ReadVia: "the operator's public pricing page", Checked: "2026-09-01",
			Label: "As listed by " + r.network + "; may be out of date; last checked 2026-09-01.",
		})
	}
	yes := true
	return map[string]any{
		"settings_offpeak.json":          offpeakResponse{Schedule: sched, Configured: true, BaseRate: 0.18, Currency: "USD"},
		"settings_offpeak_empty.json":    offpeakResponse{Schedule: offpeak.Schedule{TZ: "", Windows: []offpeak.Window{}}, BaseRate: 0.18, Currency: "USD"},
		"error_offpeak_field.json":       ferr,
		"settings_networks.json":         nets,
		"settings_membership_terms.json": map[string]any{"terms": terms},
		"settings_planner.json":          settings.PlannerPrefs{DriveMode: "EVERYDAY", HasAdapter: &yes},
	}
}

func TestSettingsFixturesAreTheServersOwnAnswers(t *testing.T) {
	write := os.Getenv("RIVOLT_WRITE_FIXTURES") != ""
	for name, v := range settingsFixtures(t) {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		path := filepath.Join(tripFixturesDir, name)
		if write {
			if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (regenerate with RIVOLT_WRITE_FIXTURES=1)", name, err)
		}
		if !bytes.Equal(want, buf.Bytes()) {
			t.Errorf("%s differs from what the server marshals now; regenerate with RIVOLT_WRITE_FIXTURES=1 go test ./internal/api -run TestSettingsFixtures", name)
		}
	}
}
