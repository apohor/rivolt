package analytics

import (
	"testing"
)

// TestClusterCharges_HomeAndPublic verifies the common case: a pile of
// low-power charges at home, a handful of slow charges elsewhere, and
// some fast stops on a road trip. Largest slow cluster -> Home; every
// other located slow session -> Public; fast sessions collapse into a
// single Fast bucket regardless of location.
func TestClusterCharges_HomeAndPublic(t *testing.T) {
	home := []ChargePoint{
		{ID: "h1", Lat: 37.77490, Lon: -122.41940, EnergyAddedKWh: 30, MaxPowerKW: 11},
		{ID: "h2", Lat: 37.77491, Lon: -122.41939, EnergyAddedKWh: 28, MaxPowerKW: 11},
		{ID: "h3", Lat: 37.77492, Lon: -122.41941, EnergyAddedKWh: 25, MaxPowerKW: 11},
		{ID: "h4", Lat: 37.77489, Lon: -122.41942, EnergyAddedKWh: 32, MaxPowerKW: 11},
		{ID: "h5", Lat: 37.77493, Lon: -122.41938, EnergyAddedKWh: 29, MaxPowerKW: 11},
	}
	pub := []ChargePoint{
		{ID: "p1", Lat: 37.80500, Lon: -122.38000, EnergyAddedKWh: 10, MaxPowerKW: 7},
		{ID: "p2", Lat: 37.80501, Lon: -122.38002, EnergyAddedKWh: 12, MaxPowerKW: 7},
		{ID: "p3", Lat: 37.80499, Lon: -122.38001, EnergyAddedKWh: 8, MaxPowerKW: 7},
	}
	fast := []ChargePoint{
		{ID: "f1", Lat: 38.50000, Lon: -121.50000, EnergyAddedKWh: 45, MaxPowerKW: 150},
		{ID: "f2", Lat: 39.10000, Lon: -120.20000, EnergyAddedKWh: 50, MaxPowerKW: 200},
	}
	all := append(append(append([]ChargePoint{}, home...), pub...), fast...)

	clusters := ClusterCharges(all, DefaultParams())

	var gotHome, gotFast *Cluster
	pubSessions := 0
	for i := range clusters {
		c := &clusters[i]
		switch c.Label {
		case LabelHome:
			gotHome = c
		case LabelFast:
			gotFast = c
		case LabelPublic:
			pubSessions += c.Sessions
		}
	}
	if gotHome == nil {
		t.Fatalf("no Home cluster produced; got %+v", clusters)
	}
	if gotHome.Sessions != len(home) {
		t.Errorf("Home sessions = %d, want %d", gotHome.Sessions, len(home))
	}
	if pubSessions != len(pub) {
		t.Errorf("Public total sessions = %d, want %d", pubSessions, len(pub))
	}
	if gotFast == nil {
		t.Fatalf("no Fast cluster produced")
	}
	if gotFast.Sessions != len(fast) {
		t.Errorf("Fast sessions = %d, want %d", gotFast.Sessions, len(fast))
	}
}

// TestClusterCharges_FastBeatsLocation: a DCFC session happening at
// the driveway coordinates is still classified as Fast, not folded
// into Home. Power is the primary axis.
func TestClusterCharges_FastBeatsLocation(t *testing.T) {
	pts := []ChargePoint{
		{ID: "h1", Lat: 37.77490, Lon: -122.41940, MaxPowerKW: 11},
		{ID: "h2", Lat: 37.77491, Lon: -122.41939, MaxPowerKW: 11},
		{ID: "f1", Lat: 37.77490, Lon: -122.41940, MaxPowerKW: 150},
	}
	clusters := ClusterCharges(pts, DefaultParams())

	var homeSessions, fastSessions int
	for _, c := range clusters {
		switch c.Label {
		case LabelHome:
			homeSessions = c.Sessions
		case LabelFast:
			fastSessions = c.Sessions
		}
	}
	if homeSessions != 2 {
		t.Errorf("Home sessions = %d, want 2 (two L2 sessions)", homeSessions)
	}
	if fastSessions != 1 {
		t.Errorf("Fast sessions = %d, want 1", fastSessions)
	}
}

// TestClusterCharges_UnknownPowerGoesToLocation: MaxPowerKW==0 means
// we don't know the peak, so the charge falls through to location
// clustering rather than being silently treated as Fast.
func TestClusterCharges_UnknownPowerGoesToLocation(t *testing.T) {
	pts := []ChargePoint{
		{ID: "a", Lat: 37.77490, Lon: -122.41940, MaxPowerKW: 0},
		{ID: "b", Lat: 37.77491, Lon: -122.41939, MaxPowerKW: 0},
	}
	clusters := ClusterCharges(pts, DefaultParams())
	if len(clusters) != 1 {
		t.Fatalf("want 1 cluster, got %d", len(clusters))
	}
	if clusters[0].Label != LabelHome {
		t.Errorf("label = %q, want Home", clusters[0].Label)
	}
}

// TestClusterCharges_MissingGPS routes slow charges with no fix into
// the Unknown bucket rather than dropping them.
func TestClusterCharges_MissingGPS(t *testing.T) {
	pts := []ChargePoint{
		{ID: "a", Lat: 0, Lon: 0, EnergyAddedKWh: 20, MaxPowerKW: 11},
		{ID: "b", Lat: 0, Lon: 0, EnergyAddedKWh: 18, MaxPowerKW: 11},
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
	d := haversineMeters(37.7749, -122.4194, 34.0522, -118.2437)
	if d < 550_000 || d > 570_000 {
		t.Errorf("SF↔LA distance = %.0f m, want ~559000", d)
	}
}
