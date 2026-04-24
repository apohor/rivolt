package analytics

import (
	"testing"
)

// TestClusterCharges_HomeAndPublic verifies the common case: a pile of
// charges in the driveway, a couple at the office, and two one-offs on
// the freeway. Home + Work should be labelled, freeway stops become
// single-session Public clusters.
func TestClusterCharges_HomeAndPublic(t *testing.T) {
	// Home cluster: 5 sessions at the same driveway (jitter ~20m).
	home := []ChargePoint{
		{ID: "h1", Lat: 37.77490, Lon: -122.41940, EnergyAddedKWh: 30},
		{ID: "h2", Lat: 37.77491, Lon: -122.41939, EnergyAddedKWh: 28},
		{ID: "h3", Lat: 37.77492, Lon: -122.41941, EnergyAddedKWh: 25},
		{ID: "h4", Lat: 37.77489, Lon: -122.41942, EnergyAddedKWh: 32},
		{ID: "h5", Lat: 37.77493, Lon: -122.41938, EnergyAddedKWh: 29},
	}
	// Work cluster: 3 sessions at the office (~5km north-east of home).
	work := []ChargePoint{
		{ID: "w1", Lat: 37.80500, Lon: -122.38000, EnergyAddedKWh: 10},
		{ID: "w2", Lat: 37.80501, Lon: -122.38002, EnergyAddedKWh: 12},
		{ID: "w3", Lat: 37.80499, Lon: -122.38001, EnergyAddedKWh: 8},
	}
	// Two isolated road-trip stops far from everything else.
	noise := []ChargePoint{
		{ID: "n1", Lat: 38.50000, Lon: -121.50000, EnergyAddedKWh: 45},
		{ID: "n2", Lat: 39.10000, Lon: -120.20000, EnergyAddedKWh: 50},
	}
	all := append(append(append([]ChargePoint{}, home...), work...), noise...)

	clusters := ClusterCharges(all, DefaultParams())

	if len(clusters) < 3 {
		t.Fatalf("expected at least 3 clusters (home/work/noise), got %d", len(clusters))
	}
	if clusters[0].Label != LabelHome {
		t.Errorf("largest cluster should be Home, got %q", clusters[0].Label)
	}
	if clusters[0].Sessions != len(home) {
		t.Errorf("home should have %d sessions, got %d", len(home), clusters[0].Sessions)
	}
	if clusters[1].Label != LabelWork {
		t.Errorf("second cluster should be Work, got %q", clusters[1].Label)
	}
	if clusters[1].Sessions != len(work) {
		t.Errorf("work should have %d sessions, got %d", len(work), clusters[1].Sessions)
	}
	// Noise points must show up as singleton Public clusters.
	publicSingletons := 0
	for _, c := range clusters[2:] {
		if c.Label == LabelPublic && c.Sessions == 1 {
			publicSingletons++
		}
	}
	if publicSingletons != 2 {
		t.Errorf("expected 2 singleton Public clusters, got %d", publicSingletons)
	}
}

// TestClusterCharges_MissingGPS routes charges with no fix into the
// Unknown bucket rather than dropping them.
func TestClusterCharges_MissingGPS(t *testing.T) {
	pts := []ChargePoint{
		{ID: "a", Lat: 0, Lon: 0, EnergyAddedKWh: 20},
		{ID: "b", Lat: 0, Lon: 0, EnergyAddedKWh: 18},
	}
	clusters := ClusterCharges(pts, DefaultParams())
	if len(clusters) != 1 {
		t.Fatalf("expected 1 unknown cluster, got %d", len(clusters))
	}
	if clusters[0].Label != LabelUnknown {
		t.Errorf("label = %q, want Unknown", clusters[0].Label)
	}
	if clusters[0].Sessions != 2 {
		t.Errorf("sessions = %d, want 2", clusters[0].Sessions)
	}
}

// TestHaversineSanity pins the distance calc against a known pair so a
// future refactor can't silently break units (metres vs kilometres).
func TestHaversineSanity(t *testing.T) {
	// San Francisco ↔ Los Angeles ≈ 559 km great-circle.
	d := haversineMeters(37.7749, -122.4194, 34.0522, -118.2437)
	if d < 550_000 || d > 570_000 {
		t.Errorf("SF↔LA distance = %.0f m, want ~559000", d)
	}
}

// TestWorkRequiresThreeSessions: a second cluster with only two
// sessions should fall through to Public so we don't mislabel a
// single repeat L3 stop as "Work".
func TestWorkRequiresThreeSessions(t *testing.T) {
	home := []ChargePoint{
		{ID: "h1", Lat: 37.77490, Lon: -122.41940},
		{ID: "h2", Lat: 37.77491, Lon: -122.41939},
		{ID: "h3", Lat: 37.77492, Lon: -122.41941},
		{ID: "h4", Lat: 37.77489, Lon: -122.41942},
	}
	// Only 2 sessions at a second location: cluster exists (min=2) but
	// shouldn't be called "Work".
	elsewhere := []ChargePoint{
		{ID: "e1", Lat: 37.80500, Lon: -122.38000},
		{ID: "e2", Lat: 37.80501, Lon: -122.38002},
	}
	all := append(append([]ChargePoint{}, home...), elsewhere...)
	clusters := ClusterCharges(all, DefaultParams())
	if len(clusters) < 2 {
		t.Fatalf("want >=2 clusters, got %d", len(clusters))
	}
	if clusters[0].Label != LabelHome {
		t.Errorf("first = %q, want Home", clusters[0].Label)
	}
	if clusters[1].Label != LabelPublic {
		t.Errorf("second = %q, want Public (only 2 sessions)", clusters[1].Label)
	}
}
