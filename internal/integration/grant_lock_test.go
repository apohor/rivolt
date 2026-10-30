//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
)

// grantLock is internal/db's grant lock key ("rv_gran").
const grantLock int64 = 0x72765f6772616e

// The role grants run one replica at a time: while another session holds the grant lock a
// GrantReadonly waits (it does not skip: each replica applies its own list), finishes once the lock
// is free, and many at once (a rollout booting N pods) all succeed.
func TestGrants_OneReplicaAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, startPostgres(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	// Roles are cluster-wide and the test databases share a server: a name per test.
	role := "ro_grant_lock_" + randHex(t, 6)
	appRole := "rivolt_app_lock_" + randHex(t, 6)
	for _, r := range []string{role, appRole} {
		if _, err := pool.ExecContext(ctx, `CREATE ROLE `+r+` NOLOGIN`); err != nil {
			t.Fatal(err)
		}
	}

	other, err := pool.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, grantLock); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		ok, err := db.GrantReadonly(ctx, pool, role)
		if err == nil && !ok {
			t.Errorf("GrantReadonly: role reported missing")
		}
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("GrantReadonly finished (%v) while another replica held the grant lock; want it to wait", err)
	case <-time.After(700 * time.Millisecond):
	}
	if _, err := other.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, grantLock); err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("GrantReadonly after the lock was freed: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("GrantReadonly still waiting after the lock was freed")
	}

	// A rollout: many replicas at once, both grant sets (EnsureAppGrants and EnsureReadonlyGrants
	// call these), all succeed.
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := db.GrantReadonly(ctx, pool, role); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			if ok, err := db.GrantApp(ctx, pool, appRole); err != nil || !ok {
				errs <- fmt.Errorf("GrantApp = %v, %v", ok, err)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent grants: %v", err)
	}
	// The lock is free afterwards.
	var got bool
	if err := pool.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, grantLock).Scan(&got); err != nil || !got {
		t.Errorf("grant lock still held after the grants: %v %v", got, err)
	}
}
