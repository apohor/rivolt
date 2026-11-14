//go:build integration

package rivian

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The document lives in the flags table: nothing before the first request, a booked run survives a read from another connection, and a
// second request while it runs is refused even when two admins click at once.
func TestFlagsSimStoreBooksOneRunAtATime(t *testing.T) {
	pool := ownerTestPool(t)
	ctx := context.Background()
	if _, err := pool.ExecContext(ctx, `DELETE FROM flags WHERE name = $1`, simFlagName); err != nil {
		t.Fatal(err)
	}
	store := NewFlagsSimStore(pool)
	if st, err := store.Load(ctx); err != nil || len(st.Runs) != 0 {
		t.Fatalf("empty store: %+v, %v", st, err)
	}
	start := simStartFor(t, SimDrive)
	ctl := NewSimControl(store)
	ctl.now = func() time.Time { return start.Add(-simLead - time.Second) }

	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := ctl.Start(ctx, SimDrive); errs <- err }()
	}
	var ok, busy int
	for i := 0; i < 2; i++ {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, ErrSimBusy):
			busy++
		default:
			t.Fatal(err)
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("two clicks: %d booked, %d refused as busy", ok, busy)
	}
	st, err := store.Load(ctx)
	if err != nil || len(st.Runs) != 1 || st.Runs[0].Kind != SimDrive {
		t.Fatalf("stored: %+v, %v", st, err)
	}
	if a, err := ctl.Active(ctx); err != nil || a == nil || a.ID != st.Runs[0].ID {
		t.Fatalf("active: %+v, %v", a, err)
	}
}
