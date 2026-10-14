package demo

import (
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestEnabledNeedsReplayClient(t *testing.T) {
	for _, c := range []struct {
		demo, client string
		want         bool
	}{
		{"", "replay", false},
		{"true", "", false},
		{"true", "live", false},
		{"true", "stub", false},
		{"true", "replay", true},
		{"TRUE", "REPLAY", true},
	} {
		t.Setenv("RIVOLT_DEMO", c.demo)
		t.Setenv("RIVIAN_CLIENT", c.client)
		if got := Enabled(); got != c.want {
			t.Errorf("%+v: Enabled = %v", c, got)
		}
	}
}

// The seed writes rivian's replay marker; it must match what a replay
// account actually snapshots.
func TestReplaySessionTokenMatchesRivian(t *testing.T) {
	c := rivian.NewReplayClient(&rivian.ReplayFeed{}, UserID())
	if err := c.Login(nil, rivian.Credentials{Email: "x@y.z", Password: "p"}); err != nil { //nolint:staticcheck
		t.Fatal(err)
	}
	if got := c.Snapshot().UserSessionToken; got != replaySessionToken {
		t.Fatalf("token = %q, want %q", got, replaySessionToken)
	}
}

func TestSeedChargesCoverCostSourcesAndAreStable(t *testing.T) {
	now := time.Date(2026, 10, 14, 9, 30, 0, 0, time.UTC)
	a := SeedCharges(UserID(), now)
	b := SeedCharges(UserID(), now.Add(5*time.Hour)) // same day
	if len(a) != 4 {
		t.Fatalf("len = %d", len(a))
	}
	sources := map[string]bool{}
	for i, c := range a {
		sources[c.CostSource] = true
		if !c.StartedAt.Equal(b[i].StartedAt) || c.ID != b[i].ID {
			t.Errorf("%s not stable within a day", c.ID)
		}
		if c.VehicleID != VehicleID() {
			t.Errorf("%s vehicle = %s", c.ID, c.VehicleID)
		}
		age := now.Sub(c.StartedAt)
		if age < 49*24*time.Hour || age > 61*24*time.Hour {
			t.Errorf("%s is %v old; must sit before the 45-day corpus", c.ID, age)
		}
	}
	for _, want := range []string{"rivian", "user", "home_rate", ""} { // "" = pending/unpriced
		if !sources[want] {
			t.Errorf("missing cost source %q", want)
		}
	}
}
