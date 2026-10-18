//go:build integration

package api

import (
	"context"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// The per-user gap is shared by every replica: a second guard (another pod)
// is refused while the first holds the user's slot, a broken run gives the slot
// back, backup and restore keep separate slots, and the per-pod semaphore
// still bounds concurrency.
func TestBackupGapIsGlobalAcrossReplicas(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "gap-user")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUser(ctx, pool, "gap-other")
	if err != nil {
		t.Fatal(err)
	}
	const gap = 10 * time.Minute
	podA := newDBBackupGuard(2, pool, "last_backup_at")
	podB := newDBBackupGuard(2, pool, "last_backup_at")

	relA, _, ok := podA.acquire(ctx, uid, gap)
	if !ok {
		t.Fatal("first backup refused")
	}
	if _, wait, ok := podB.acquire(ctx, uid, gap); ok || wait < 9*time.Minute || wait > gap {
		t.Fatalf("second pod: ok=%v wait=%v, want refused with about 10 minutes", ok, wait)
	}
	if _, _, ok := podA.acquire(ctx, uid, gap); ok {
		t.Fatal("the same pod let the same user through twice")
	}
	// Another user is unaffected.
	if rel, _, ok := podB.acquire(ctx, other, gap); !ok {
		t.Fatal("another user refused")
	} else {
		rel()
	}
	// A broken run gives the slot back, on any pod.
	relA()
	podB.forget(uid)
	rel, _, ok := podA.acquire(ctx, uid, gap)
	if !ok {
		t.Fatal("retry refused after forget")
	}
	rel()

	// Restore has its own slot.
	restoreGuard := newDBBackupGuard(1, pool, "last_restore_at")
	if rel, _, ok := restoreGuard.acquire(ctx, uid, gap); !ok {
		t.Fatal("restore refused although only a backup ran")
	} else {
		rel()
	}
	// The gap passes: after it, the user may start again.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET last_backup_at = now() - interval '11 minutes' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if rel, _, ok := podB.acquire(ctx, uid, gap); !ok {
		t.Fatal("refused after the gap passed")
	} else {
		rel()
	}

	// The semaphore is per pod and checked before the database is touched.
	one := newDBBackupGuard(1, pool, "last_backup_at")
	hold, _, ok := one.acquire(ctx, other, 0)
	if !ok {
		t.Fatal("first slot refused")
	}
	if _, wait, ok := one.acquire(ctx, uid, 0); ok || wait != 30*time.Second {
		t.Fatalf("full pod: ok=%v wait=%v, want refused with 30s", ok, wait)
	}
	hold()
}
