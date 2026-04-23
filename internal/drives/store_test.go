package drives

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(filepath.Join(t.TempDir(), "drives.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sampleDrive(id string, startedAt time.Time) Drive {
	return Drive{
		ID:              id,
		VehicleID:       "veh-1",
		StartedAt:       startedAt,
		EndedAt:         startedAt.Add(30 * time.Minute),
		StartSoCPct:     80,
		EndSoCPct:       72,
		StartOdometerMi: 1000,
		EndOdometerMi:   1025.6,
		DistanceMi:      25.6,
		StartLat:        37.7749,
		StartLon:        -122.4194,
		EndLat:          37.8044,
		EndLon:          -122.2712,
		MaxSpeedMph:     72.1,
		AvgSpeedMph:     48.3,
		Source:          "electrafi_import",
	}
}

func TestUpsertRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	in := sampleDrive("d-1", t0)
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
	d := got[0]
	if d.ID != in.ID || d.VehicleID != in.VehicleID ||
		!d.StartedAt.Equal(in.StartedAt) || !d.EndedAt.Equal(in.EndedAt) ||
		d.StartSoCPct != in.StartSoCPct || d.EndSoCPct != in.EndSoCPct ||
		d.StartOdometerMi != in.StartOdometerMi || d.EndOdometerMi != in.EndOdometerMi ||
		d.DistanceMi != in.DistanceMi ||
		d.StartLat != in.StartLat || d.StartLon != in.StartLon ||
		d.EndLat != in.EndLat || d.EndLon != in.EndLon ||
		d.MaxSpeedMph != in.MaxSpeedMph || d.AvgSpeedMph != in.AvgSpeedMph ||
		d.Source != in.Source {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", d, in)
	}

	// Re-upsert same ID overwrites.
	in2 := in
	in2.DistanceMi = 99.9
	if err := s.Upsert(ctx, in2); err != nil {
		t.Fatalf("Upsert 2: %v", err)
	}
	n, err := s.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Errorf("Count=%d, want 1", n)
	}
	got2, _ := s.ListRecent(ctx, 10)
	if got2[0].DistanceMi != 99.9 {
		t.Errorf("DistanceMi=%v, want 99.9", got2[0].DistanceMi)
	}
}

func TestListRecentOrderAndLimit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := s.Upsert(ctx, sampleDrive("d-"+string(rune('a'+i)), base.Add(time.Duration(i)*time.Hour))); err != nil {
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
	for i := 0; i+1 < len(got); i++ {
		if !got[i].StartedAt.After(got[i+1].StartedAt) {
			t.Errorf("not DESC: %v !after %v", got[i].StartedAt, got[i+1].StartedAt)
		}
	}
}

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
