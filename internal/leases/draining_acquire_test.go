package leases

import (
	"context"
	"database/sql"
	"testing"
)

// A draining pod acquires nothing, and says so before it touches the database (design 4.5): acquire-on-request on a draining pod is a 503,
// never a lease row. The store here has no database at all, so any query would panic rather than pass.
func TestDrainingPodNeverAcquires(t *testing.T) {
	s, err := NewStore(new(sql.DB), "pod-a")
	if err != nil {
		t.Fatal(err)
	}
	s.draining = true
	got, err := s.AcquireGroups(context.Background(), []string{"00000000-0000-0000-0000-000000000001"})
	if err != nil || len(got) != 0 {
		t.Fatalf("a draining pod got %v (err %v), want nothing", got, err)
	}
}
