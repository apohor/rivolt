package samples

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "samples.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleAt(at time.Time) Sample {
	return Sample{
		VehicleID:       "veh-1",
		At:              at,
		BatteryLevelPct: 72.5,
		RangeMi:         210.3,
		OdometerMi:      12345.6,
		Lat:             37.7749,
		Lon:             -122.4194,
		SpeedMph:        42.1,
		ShiftState:      "D",
		ChargingState:   "Disconnected",
		ChargerPowerKW:  0,
		ChargeLimitPct:  80,
		InsideTempC:     22.5,
		OutsideTempC:    18.0,
		DriveNumber:     42,
		ChargeNumber:    0,
		Source:          "electrafi_import",
	}
}

// InsertBatch round-trips the full row shape and orders by time asc.
func TestInsertBatchRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	batch := []Sample{
		sampleAt(base.Add(2 * time.Minute)),
		sampleAt(base.Add(1 * time.Minute)),
		sampleAt(base),
	}
	if err := s.InsertBatch(ctx, batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count=%d, want 3", n)
	}

	got, err := s.ListSince(ctx, base.Add(-time.Hour), 100)
	if err != nil {
		t.Fatalf("ListSince: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len=%d, want 3", len(got))
	}
	// ASC order.
	for i := 0; i+1 < len(got); i++ {
		if !got[i].At.Before(got[i+1].At) {
			t.Errorf("not ASC: %v !before %v", got[i].At, got[i+1].At)
		}
	}
	// Spot-check field preservation on the first row.
	first := got[0]
	want := sampleAt(base)
	if first.VehicleID != want.VehicleID || first.BatteryLevelPct != want.BatteryLevelPct ||
		first.RangeMi != want.RangeMi || first.OdometerMi != want.OdometerMi ||
		first.Lat != want.Lat || first.Lon != want.Lon ||
		first.SpeedMph != want.SpeedMph || first.ShiftState != want.ShiftState ||
		first.ChargingState != want.ChargingState || first.ChargerPowerKW != want.ChargerPowerKW ||
		first.ChargeLimitPct != want.ChargeLimitPct ||
		first.InsideTempC != want.InsideTempC || first.OutsideTempC != want.OutsideTempC ||
		first.DriveNumber != want.DriveNumber || first.ChargeNumber != want.ChargeNumber ||
		first.Source != want.Source {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", first, want)
	}
}

// ListSince filters by the since cursor, exclusive lower bound.
func TestListSinceFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	batch := []Sample{
		sampleAt(base),
		sampleAt(base.Add(time.Minute)),
		sampleAt(base.Add(2 * time.Minute)),
	}
	if err := s.InsertBatch(ctx, batch); err != nil {
		t.Fatalf("InsertBatch: %v", err)
	}
	got, err := s.ListSince(ctx, base, 100)
	if err != nil {
		t.Fatalf("ListSince: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len=%d, want 2 (since is strict >, excludes base)", len(got))
	}
}

// Re-inserting the same (vehicle_id, at) is a no-op (INSERT OR IGNORE).
// This is what makes re-imports idempotent.
func TestInsertBatchIgnoresDuplicates(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	row := sampleAt(base)
	if err := s.InsertBatch(ctx, []Sample{row}); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Try to re-insert the same pk with a different payload.
	dup := row
	dup.BatteryLevelPct = 99.9
	if err := s.InsertBatch(ctx, []Sample{dup}); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	n, _ := s.Count(ctx)
	if n != 1 {
		t.Errorf("Count=%d, want 1 (duplicate pk should be ignored)", n)
	}
	got, _ := s.ListSince(ctx, base.Add(-time.Hour), 10)
	if got[0].BatteryLevelPct != 72.5 {
		t.Errorf("BatteryLevelPct=%v, want 72.5 (original should win)", got[0].BatteryLevelPct)
	}
}

// Empty batch is fine — returns nil without touching the DB.
func TestInsertBatchEmpty(t *testing.T) {
	s := openTestStore(t)
	if err := s.InsertBatch(context.Background(), nil); err != nil {
		t.Errorf("InsertBatch(nil) err=%v", err)
	}
}
