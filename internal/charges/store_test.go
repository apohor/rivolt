package charges

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "charges.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleCharge(id string, startedAt time.Time) Charge {
	return Charge{
		ID:             id,
		VehicleID:      "veh-1",
		StartedAt:      startedAt,
		EndedAt:        startedAt.Add(45 * time.Minute),
		StartSoCPct:    40.0,
		EndSoCPct:      70.0,
		EnergyAddedKWh: 24.5,
		MilesAdded:     87.3,
		MaxPowerKW:     7.5,
		AvgPowerKW:     7.2,
		FinalState:     "Complete",
		Lat:            37.7749,
		Lon:            -122.4194,
		Source:         "electrafi_import",
	}
}

// Upsert should insert on first call and overwrite on a second call
// with the same primary key.
func TestUpsertRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	in := sampleCharge("c-1", t0)
	if err := s.Upsert(ctx, in); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := s.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len=%d, want 1", len(got))
	}
	c := got[0]
	if c.ID != in.ID || c.VehicleID != in.VehicleID ||
		!c.StartedAt.Equal(in.StartedAt) || !c.EndedAt.Equal(in.EndedAt) ||
		c.StartSoCPct != in.StartSoCPct || c.EndSoCPct != in.EndSoCPct ||
		c.EnergyAddedKWh != in.EnergyAddedKWh || c.MilesAdded != in.MilesAdded ||
		c.MaxPowerKW != in.MaxPowerKW || c.AvgPowerKW != in.AvgPowerKW ||
		c.FinalState != in.FinalState || c.Lat != in.Lat || c.Lon != in.Lon ||
		c.Source != in.Source {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", c, in)
	}

	// Second upsert with same ID replaces.
	in2 := in
	in2.EnergyAddedKWh = 99.9
	in2.FinalState = "charging_station_err"
	if err := s.Upsert(ctx, in2); err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Errorf("Count=%d, want 1 (upsert should not duplicate)", n)
	}
	got2, _ := s.ListRecent(ctx, 10)
	if got2[0].EnergyAddedKWh != 99.9 || got2[0].FinalState != "charging_station_err" {
		t.Errorf("second upsert did not overwrite: %+v", got2[0])
	}
}

// ListRecent orders by started_at DESC and honours limit.
func TestListRecentOrderAndLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		c := sampleCharge("c-"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Hour))
		if err := s.Upsert(ctx, c); err != nil {
			t.Fatalf("Upsert %d: %v", i, err)
		}
	}

	got, err := s.ListRecent(ctx, 3)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d, want 3", len(got))
	}
	// Newest first.
	for i := 0; i+1 < len(got); i++ {
		if !got[i].StartedAt.After(got[i+1].StartedAt) {
			t.Errorf("not DESC: %v !after %v", got[i].StartedAt, got[i+1].StartedAt)
		}
	}
	// Newest should be base+4h.
	wantTop := base.Add(4 * time.Hour)
	if !got[0].StartedAt.Equal(wantTop) {
		t.Errorf("top=%v, want %v", got[0].StartedAt, wantTop)
	}
}

// Limit <= 0 falls back to 50; limit > 10_000 clamps to 10_000. We
// don't insert 10k rows — just check the fallback for <= 0 returns all
// 3 rows (would be zero without the clamp).
func TestListRecentLimitFallback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		_ = s.Upsert(ctx, sampleCharge("c-"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Hour)))
	}
	got, err := s.ListRecent(ctx, 0)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("len=%d, want 3 (limit=0 should fall back, not clamp to 0)", len(got))
	}
}

// Count on an empty store returns 0.
func TestCountEmpty(t *testing.T) {
	s := openTestStore(t)
	n, err := s.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Errorf("Count=%d, want 0", n)
	}
}
