//go:build integration

package drives_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/testpg"
)

// A drive upsert with a fence token is written only while the group lease is valid, in the same
// statement. A stale owner's close is refused and the new owner's continuation is kept; a guarded
// write stores exactly what the plain one does.
func TestUpsert_FencedByTheGroupLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// One pool: testpg.DSN gives each call its own database, so the lease row and the store must
	// share this one.
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "drivefence")
	if err != nil {
		t.Fatal(err)
	}
	store := drives.NewFactory(pool, db.NewVehicleResolverFactory(pool)).For(uid)

	var epochA int64
	if err := pool.QueryRowContext(ctx, `
		INSERT INTO account_leases (group_id, pod_id, epoch, acquired_at, renewed_at, expires_at)
		VALUES ($1, 'pod-a', nextval('account_lease_epoch'), now(), now(), now() + interval '30 seconds')
		RETURNING epoch`, uid).Scan(&epochA); err != nil {
		t.Fatal(err)
	}
	tokA := fence.Token{Group: uid.String(), Pod: "pod-a", Epoch: epochA}
	start := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	// Parity: the same drive through the plain and the guarded statement reads back the same.
	plain := sampleDrive("ext-plain", "rv-fence", start)
	guarded := sampleDrive("ext-guarded", "rv-fence", start.Add(3*time.Hour))
	if err := store.Upsert(ctx, plain); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(fence.With(ctx, tokA), guarded); err != nil {
		t.Fatalf("guarded upsert with a valid lease: %v", err)
	}
	got, err := store.ListRecent(ctx, 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("ListRecent = %d drives, %v", len(got), err)
	}
	byID := map[string]int{"ext-plain": -1, "ext-guarded": -1}
	for i, d := range got {
		byID[d.ID] = i
	}
	p, g := got[byID["ext-plain"]], got[byID["ext-guarded"]]
	g.ID, g.StartedAt, g.EndedAt = p.ID, p.StartedAt, p.EndedAt
	g.Thinned, p.Thinned = false, false
	if g != p {
		t.Errorf("the guarded write stored something different:\n plain   %+v\n guarded %+v", p, g)
	}

	// An update by the owner (the drive keeps running).
	run := sampleDrive("ext-guarded", "rv-fence", start.Add(3*time.Hour))
	run.EndSoCPct = 65
	if err := store.Upsert(fence.With(ctx, tokA), run); err != nil {
		t.Fatalf("owner update: %v", err)
	}

	// A stale epoch, another pod and a missing lease are refused, and change nothing.
	closed := run
	closed.EndSoCPct = 10
	for name, bad := range map[string]fence.Token{
		"stale epoch": {Group: tokA.Group, Pod: "pod-a", Epoch: epochA - 1},
		"other pod":   {Group: tokA.Group, Pod: "pod-b", Epoch: epochA},
		"no lease":    {Group: "00000000-0000-0000-0000-000000000001", Pod: "pod-a", Epoch: epochA},
	} {
		if err := store.Upsert(fence.With(ctx, bad), closed); !errors.Is(err, fence.ErrFenced) {
			t.Errorf("%s: err = %v, want ErrFenced", name, err)
		}
	}
	endSoC := func() float64 {
		got, err := store.ListRecent(ctx, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range got {
			if d.ID == "ext-guarded" {
				return d.EndSoCPct
			}
		}
		t.Fatal("drive missing")
		return 0
	}
	if v := endSoC(); v != 65 {
		t.Fatalf("a refused write changed the drive: end SoC %v, want 65", v)
	}

	// A steal: pod-b takes the lease. The old owner's close is refused; the new owner's continuation is kept.
	var epochB int64
	if err := pool.QueryRowContext(ctx, `UPDATE account_leases SET pod_id = 'pod-b', epoch = nextval('account_lease_epoch'), expires_at = now() + interval '30 seconds' WHERE group_id = $1 RETURNING epoch`, uid).Scan(&epochB); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(fence.With(ctx, tokA), closed); !errors.Is(err, fence.ErrFenced) {
		t.Errorf("old owner's close after a steal: err = %v, want ErrFenced", err)
	}
	cont := run
	cont.EndSoCPct = 60
	if err := store.Upsert(fence.With(ctx, fence.Token{Group: tokA.Group, Pod: "pod-b", Epoch: epochB}), cont); err != nil {
		t.Errorf("new owner's continuation: %v", err)
	}
	if v := endSoC(); v != 60 {
		t.Errorf("end SoC %v, want 60 (the new owner's continuation, not the old owner's close)", v)
	}
}
