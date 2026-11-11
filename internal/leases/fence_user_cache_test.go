package leases

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

func cachedStore(t *testing.T, calls *int, gid string, ok bool, err error) *Store {
	t.Helper()
	s, e := NewStore(new(sql.DB), "pod-a")
	if e != nil {
		t.Fatal(e)
	}
	s.SetGroupLead(true)
	s.lookupUserGroup = func(context.Context, string) (string, bool, error) { *calls++; return gid, ok, err }
	return s
}

// The token gate asks FenceUser on every For/Raw: the user -> group query runs once per userGroupTTL per user, while the held state is read
// from memory each time.
func TestFenceUserCachesTheUserGroup(t *testing.T) {
	calls := 0
	s := cachedStore(t, &calls, "g1", true, nil)
	for i := 0; i < 50; i++ {
		if _, st, err := s.FenceUser(context.Background(), "u1"); err != nil || st != fence.Lost {
			t.Fatalf("FenceUser = %v, %v; want Lost (the group is not held)", st, err)
		}
	}
	if calls != 1 {
		t.Fatalf("lookups = %d, want 1", calls)
	}
	// The held state is not cached: holding the group changes the answer at once.
	s.groupMu.Lock()
	s.groups = map[string]int64{"g1": 7}
	s.groupOKAt = time.Now()
	s.groupMu.Unlock()
	if tok, st, _ := s.FenceUser(context.Background(), "u1"); st != fence.Held || tok.Group != "g1" {
		t.Fatalf("after holding g1: %v %v, want Held", tok, st)
	}
	if calls != 1 {
		t.Fatalf("lookups = %d after the group became held, want still 1", calls)
	}
	// An entry older than the TTL is read again.
	s.userGroupMu.Lock()
	e := s.userGroup["u1"]
	e.at = time.Now().Add(-2 * userGroupTTL)
	s.userGroup["u1"] = e
	s.userGroupMu.Unlock()
	s.FenceUser(context.Background(), "u1")
	if calls != 2 {
		t.Fatalf("lookups = %d after expiry, want 2", calls)
	}
}

func TestFenceUserDoesNotCacheAnError(t *testing.T) {
	calls := 0
	s := cachedStore(t, &calls, "", false, errors.New("db"))
	for i := 0; i < 3; i++ {
		if _, _, err := s.FenceUser(context.Background(), "u1"); err == nil {
			t.Fatal("want the lookup error")
		}
	}
	if calls != 3 {
		t.Fatalf("lookups = %d, want 3 (errors are not cached)", calls)
	}
}

func TestFenceUserCachesAUserWithNoGroup(t *testing.T) {
	calls := 0
	s := cachedStore(t, &calls, "", false, nil)
	for i := 0; i < 5; i++ {
		if _, st, err := s.FenceUser(context.Background(), "u1"); err != nil || st != fence.Lost {
			t.Fatalf("FenceUser = %v, %v; want Lost", st, err)
		}
	}
	if calls != 1 {
		t.Fatalf("lookups = %d, want 1", calls)
	}
}
