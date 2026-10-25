package api

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/apohor/rivolt/internal/demo"
)

// The iPhone's demo Plan tab opens the sample trips the server already serves (GET /api/demo/trips and its /plan): its fixtures are
// those bytes verbatim, so a change to the embedded samples fails here until they are regenerated (RIVOLT_WRITE_FIXTURES=1).
func TestDemoTripFixturesAreTheEmbeddedBytes(t *testing.T) {
	plan := func(id string) []byte {
		b, ok := demo.PresetPlan(id)
		if !ok {
			t.Fatalf("no sample plan %q", id)
		}
		return b.Body
	}
	for name, want := range map[string][]byte{
		"demo_trips.json":              demo.PresetsIndex().Body,
		"demo_trip_plan_single.json":   plan("mammoth"),
		"demo_trip_plan_two_days.json": plan("919ca13a-123f-5423-994d-12d7ccd31319"),
	} {
		path := filepath.Join(tripFixturesDir, name)
		if os.Getenv("RIVOLT_WRITE_FIXTURES") != "" {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v (RIVOLT_WRITE_FIXTURES=1 writes it)", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is not what the server embeds any more: RIVOLT_WRITE_FIXTURES=1 go test ./internal/api -run TestDemoTripFixtures", name)
		}
	}
}
