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

// TestPhantomCharges_CleanupOnOpen seeds a DB with one healthy charge
// and three phantom rows (StartSoC == EndSoC), closes the store, then
// re-opens it. The second open triggers applyDataMigrations, which
// should delete the three phantoms and record the migration. A third
// open must be a no-op so the migration doesn't replay against rows
// added *after* v0.3.54 that happen to satisfy the same predicate
// (unlikely, but worth being defensive about).
func TestPhantomCharges_CleanupOnOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "charges.db")

	// First open seeds the DB and — because the migrations table is
	// empty — also sweeps the phantoms in the same go. That's the
	// real-world path too: the first container boot on a DB that was
	// created by an older rivolt will seed migrations + clean in one
	// call. We work around it here by recording the migration up front
	// so the first open is purely a seed pass, then clearing the
	// record so the second open performs the clean.
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore #1: %v", err)
	}
	ctx := context.Background()
	good := sampleCharge("good-1", time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC))
	if err := s.Upsert(ctx, good); err != nil {
		t.Fatalf("upsert good: %v", err)
	}
	for i, pid := range []string{"phantom-a", "phantom-b", "phantom-c"} {
		p := sampleCharge(pid, time.Date(2026, 4, 2+i, 9, 0, 0, 0, time.UTC))
		p.StartSoCPct = 52.0
		p.EndSoCPct = 52.0 // 0 pp delta → phantom
		p.EnergyAddedKWh = 25.7
		if err := s.Upsert(ctx, p); err != nil {
			t.Fatalf("upsert %s: %v", pid, err)
		}
	}
	// Clear the recorded migration so a subsequent OpenStore re-runs
	// the cleanup. Without this the first OpenStore would already
	// have applied (and the test would just be checking the marker).
	if _, err := s.db.Exec("DELETE FROM migrations"); err != nil {
		t.Fatalf("clear migrations: %v", err)
	}
	s.Close()

	// Second open: migration fires, phantoms gone, marker recorded.
	s2, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore #2: %v", err)
	}
	got, err := s2.ListRecent(ctx, 50)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(got) != 1 || got[0].ID != "good-1" {
		t.Fatalf("after migration: got %d rows (ids=%v), want 1 (good-1)",
			len(got), chargeIDs(got))
	}
	// Marker must be present so the next boot is idempotent.
	var n int
	if err := s2.db.QueryRow(
		"SELECT count(*) FROM migrations WHERE id = ?",
		"2026-04-phantom-charges-v1",
	).Scan(&n); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if n != 1 {
		t.Errorf("migrations marker count = %d, want 1", n)
	}

	// Third open: a fresh phantom-looking row added AFTER the
	// migration has been recorded must NOT be swept. This guards
	// against accidental future replays if the predicate widens.
	newPhantom := sampleCharge("phantom-postfix", time.Date(2026, 4, 10, 8, 0, 0, 0, time.UTC))
	newPhantom.StartSoCPct = 60
	newPhantom.EndSoCPct = 60
	if err := s2.Upsert(ctx, newPhantom); err != nil {
		t.Fatalf("upsert post-fix: %v", err)
	}
	s2.Close()

	s3, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore #3: %v", err)
	}
	defer s3.Close()
	got3, err := s3.ListRecent(ctx, 50)
	if err != nil {
		t.Fatalf("ListRecent 3: %v", err)
	}
	if len(got3) != 2 {
		t.Errorf("post-migration rows = %d, want 2 (good-1 + phantom-postfix)", len(got3))
	}
}

func chargeIDs(cs []Charge) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
