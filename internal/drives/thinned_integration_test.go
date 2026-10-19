//go:build integration

package drives_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/testpg"
)

// A drive whose month of raw telemetry has been thinned says so, on the detail
// read and on the lists; a drive in an untouched month does not, and the field
// is absent from its JSON.
func TestDriveThinnedFlag(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "thinned-flag")
	if err != nil {
		t.Fatal(err)
	}
	var vid string
	if err := pool.QueryRowContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1,'rv-thinned','R1S') RETURNING id`, uid).Scan(&vid); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// 2026-03 is thinned; 2026-04 is not. One drive crosses from March into April.
	exec(`INSERT INTO sample_tiers (month, rows_before, rows_after, bytes_before, bytes_after) VALUES ('2026-03-01', 100, 10, 1000, 100) ON CONFLICT (month) DO NOTHING`)
	d := func(id string, start, end string) {
		exec(`INSERT INTO drives (user_id, vehicle_id, external_id, started_at, ended_at, distance_mi, source) VALUES ($1,$2,$3,$4::timestamptz,$5::timestamptz,5,'live')`, uid, vid, id, start, end)
	}
	d("in-march", "2026-03-10T12:00:00Z", "2026-03-10T12:30:00Z")
	d("in-april", "2026-04-10T12:00:00Z", "2026-04-10T12:30:00Z")
	d("crosses", "2026-03-31T23:50:00Z", "2026-04-01T00:20:00Z")

	st := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)
	want := map[string]bool{"in-march": true, "in-april": false, "crosses": true}
	for id, thinned := range want {
		got, err := st.Get(ctx, id)
		if err != nil || got == nil {
			t.Fatalf("Get %s: %v %v", id, got, err)
		}
		if got.Thinned != thinned {
			t.Errorf("Get %s thinned = %v, want %v", id, got.Thinned, thinned)
		}
		b, _ := json.Marshal(got)
		if has := strings.Contains(string(b), `"thinned":true`); has != thinned {
			t.Errorf("%s JSON has thinned = %v, want %v: %s", id, has, thinned, b)
		}
	}
	for name, list := range map[string]func(context.Context, int) ([]drives.Drive, error){"ListRecent": st.ListRecent, "ListRecentSlim": st.ListRecentSlim} {
		rows, err := list(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("%s returned %d drives, want 3", name, len(rows))
		}
		for _, r := range rows {
			if r.Thinned != want[r.ID] {
				t.Errorf("%s %s thinned = %v, want %v", name, r.ID, r.Thinned, want[r.ID])
			}
		}
	}
	// The backup iterator does not carry the flag.
	if err := st.EachAll(ctx, func(r drives.Drive) error {
		if r.Thinned {
			t.Errorf("EachAll %s has Thinned set; the backup and export do not carry it", r.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
