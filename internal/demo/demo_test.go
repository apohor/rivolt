package demo

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

func TestEnabledNeedsReplayOverSampleCorpus(t *testing.T) {
	for _, c := range []struct {
		demo, client, source string
		want                 bool
	}{
		{"", "replay", "sample", false},
		{"true", "", "sample", false},
		{"true", "live", "sample", false},
		{"true", "stub", "sample", false},
		// replaying a recorded vehicle must never be public
		{"true", "replay", "", false},
		{"true", "replay", "01-242521064", false},
		{"true", "replay", "sample", true},
		{"TRUE", "REPLAY", "sample", true},
	} {
		t.Setenv("RIVOLT_DEMO", c.demo)
		t.Setenv("RIVIAN_CLIENT", c.client)
		t.Setenv("RIVOLT_REPLAY_SOURCE", c.source)
		t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "")
		if got := Enabled(); got != c.want {
			t.Errorf("%+v: Enabled = %v", c, got)
		}
	}
	// A recorded source vehicle configured alongside sample: off.
	t.Setenv("RIVOLT_DEMO", "true")
	t.Setenv("RIVIAN_CLIENT", "replay")
	t.Setenv("RIVOLT_REPLAY_SOURCE", "sample")
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "01-242521064")
	if Enabled() {
		t.Error("Enabled must be false while RIVOLT_REPLAY_SOURCE_VEHICLE is set")
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
	if len(a) != 2 {
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
	for _, want := range []string{"home_rate", ""} { // "" = pending/unpriced
		if !sources[want] {
			t.Errorf("missing cost source %q", want)
		}
	}
}

type fakePricer struct{ got map[string][2]any }

func (f *fakePricer) UpdatePricing(_ context.Context, id string, cost float64, cur string, _ float64) (int64, error) {
	if f.got == nil {
		f.got = map[string][2]any{}
	}
	f.got[id] = [2]any{cost, cur}
	return 1, nil
}

// hotelStart scans the looping corpus for a wall-clock start the sample
// says was hand-priced.
func hotelStart(t *testing.T) time.Time {
	t.Helper()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for m := 0; m < 1100*60; m++ {
		at := base.Add(time.Duration(m) * time.Minute)
		if _, _, ok := rivian.SampleUserPrice(at); ok {
			return at
		}
	}
	t.Fatal("no hand-priced session in a corpus loop")
	return time.Time{}
}

func TestPriceCandidatesPricesOnlyTheHotel(t *testing.T) {
	hotel := hotelStart(t)
	f := &fakePricer{}
	n, err := PriceCandidates(context.Background(), []PriceCandidate{
		{ID: "hotel", StartedAt: hotel},
		{ID: "other", StartedAt: hotel.Add(-9 * time.Hour)},
	}, f)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := f.got["hotel"]; got[0] != 15.0 || got[1] != "USD" {
		t.Fatalf("hotel priced %v", got)
	}
	if _, ok := f.got["other"]; ok {
		t.Fatal("a non-hand-priced session was priced")
	}
}
