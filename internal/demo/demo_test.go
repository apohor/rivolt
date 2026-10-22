package demo

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/dcfcrates"
	"github.com/apohor/rivolt/internal/packhealth"
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
		{"true", "replay", "01-000000000", false},
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
	t.Setenv("RIVOLT_REPLAY_SOURCE_VEHICLE", "01-000000000")
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

// The older history sits before the 45-day corpus, is stable within a day, and is
// enough for the trends: at least two calendar months of seeded drives with 50+
// mi each (the corpus supplies the rest), and three charges that qualify for the
// pack-capacity estimate.
func TestSeedHistoryFeedsTheTrends(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 30, 23, 0, 0, 0, time.UTC),
		time.Date(2027, 1, 1, 0, 30, 0, 0, time.UTC),
		time.Date(2027, 3, 15, 12, 0, 0, 0, time.UTC),
	} {
		ds, same := SeedDrives(UserID(), now), SeedDrives(UserID(), now.Add(20*time.Minute))
		months := map[string]float64{}
		for i, d := range ds {
			if age := now.Sub(d.StartedAt); age < 46*24*time.Hour || age > 121*24*time.Hour {
				t.Errorf("%s drive %s is %v old; must sit before the 45-day corpus and within 120 days", now.Format("2006-01-02"), d.ID, age)
			}
			if d.DistanceMi < 5 || d.EnergyUsedKWh < 1 || d.Source != "seed" || d.VehicleID != VehicleID() {
				t.Errorf("drive %+v would not count in the trend or is mis-keyed", d)
			}
			if !d.StartedAt.Equal(same[i].StartedAt) || d.ID != same[i].ID {
				t.Errorf("%s is not stable within a day", d.ID)
			}
			months[d.StartedAt.Format("2006-01")] += d.DistanceMi
		}
		enough := 0
		for _, mi := range months {
			if mi >= 50 {
				enough++
			}
		}
		if enough < 2 {
			t.Errorf("%s: only %d seeded months reach 50 mi (%v); the trend needs three with the corpus's", now.Format("2006-01-02"), enough, months)
		}
		cs := SeedDCFCharges(UserID(), now)
		if len(cs) != 3 {
			t.Fatalf("len = %d", len(cs))
		}
		for _, c := range cs {
			if age := now.Sub(c.StartedAt); age < 49*24*time.Hour {
				t.Errorf("%s is %v old; must sit before the corpus", c.ID, age)
			}
			s, ok := packhealth.Derive(packhealth.ChargeInput{ChargeID: uuid.New(), EndedAt: c.EndedAt, StartSoCPct: c.StartSoCPct, EndSoCPct: c.EndSoCPct, EnergyAddedKWh: c.EnergyAddedKWh})
			if !ok || s.PackKWhEffective < 125 || s.PackKWhEffective > 135 || c.MaxPowerKW <= packhealth.MinPeakKWForMeasured {
				t.Errorf("%s does not give a clean capacity sample: %+v %v peak %v", c.ID, s, ok, c.MaxPowerKW)
			}
			if c.CostSource != "user" || c.Cost <= 0 {
				t.Errorf("%s is not user-priced: %+v", c.ID, c)
			}
		}
	}
}

// The demo owner's rates must read as the owner's own: none may equal the built-in
// table's number (that row would be treated as never edited and priced nothing), and every
// row must survive the book's normalization.
func TestDemoNetworksAreTheOwnersOwnRates(t *testing.T) {
	rows := DemoNetworks()
	if len(rows) < 5 {
		t.Fatalf("only %d demo networks", len(rows))
	}
	for _, r := range rows {
		if r.PricePerKWh <= 0 {
			t.Errorf("%s: no rate", r.Name)
		}
		for _, n := range dcfcrates.Networks {
			if n.Slug == r.Slug && r.PricePerKWh == n.GuestRate {
				t.Errorf("%s: demo rate equals the table's %v and would read as unset", r.Name, n.GuestRate)
			}
		}
		if r.MemberPrice != 0 && r.MemberPrice >= r.PricePerKWh {
			t.Errorf("%s: member %v not below guest %v", r.Name, r.MemberPrice, r.PricePerKWh)
		}
	}
}
