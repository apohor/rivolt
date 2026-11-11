package leases

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

// FenceUser is Fence for a user rather than a vehicle (the receipts sync is per user): the token
// of the user's group when this pod holds it. One query (cached for userGroupTTL) maps the user to their group (the same
// userGroups CTE the leases use); the rest is the in-memory state Fence reads. A user with no
// vehicle has no group, so no owner, and is Lost in lead mode.
func (s *Store) FenceUser(ctx context.Context, uid string) (fence.Token, fence.State, error) {
	s.groupMu.Lock()
	lead := s.lead
	s.groupMu.Unlock()
	if !lead {
		return fence.Token{}, fence.Off, nil
	}
	gid, ok, err := s.groupOfUser(ctx, uid)
	if err != nil {
		return fence.Token{}, fence.Lost, err
	}
	if !ok {
		return fence.Token{}, fence.Lost, nil
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	tok, st := s.tokenLocked(gid)
	return tok, st, nil
}

// userGroupTTL bounds how stale the user -> group answer FenceUser reads may be. Membership changes only when a vehicle row is added or removed,
// so a few seconds is far inside what a lead pass already takes to notice; the held state itself is never cached.
const userGroupTTL = 5 * time.Second

// userGroupCacheMax is the size past which a store inserting a user -> group answer first drops the expired ones.
const userGroupCacheMax = 4096

type userGroupEntry struct {
	gid string // empty: the user has no group
	at  time.Time
}

// groupOfUser maps a user to their group: one recursive query over the vehicles table (the userGroups CTE), cached for userGroupTTL because the
// token gate asks on every For/Raw of the request path. ok is false for a user with no vehicle. An error is not cached.
func (s *Store) groupOfUser(ctx context.Context, uid string) (gid string, ok bool, err error) {
	s.userGroupMu.Lock()
	e, hit := s.userGroup[uid]
	s.userGroupMu.Unlock()
	if hit && time.Since(e.at) < userGroupTTL {
		return e.gid, e.gid != "", nil
	}
	lookup := s.lookupUserGroup
	if lookup == nil {
		lookup = s.queryUserGroup
	}
	gid, ok, err = lookup(ctx, uid)
	if err != nil {
		return "", false, err
	}
	s.userGroupMu.Lock()
	if s.userGroup == nil {
		s.userGroup = map[string]userGroupEntry{}
	}
	if len(s.userGroup) >= userGroupCacheMax {
		for u, old := range s.userGroup {
			if time.Since(old.at) >= userGroupTTL {
				delete(s.userGroup, u)
			}
		}
	}
	s.userGroup[uid] = userGroupEntry{gid: gid, at: time.Now()}
	s.userGroupMu.Unlock()
	return gid, ok, nil
}

func (s *Store) queryUserGroup(ctx context.Context, uid string) (string, bool, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, userGroups+` SELECT gid FROM grp WHERE user_id = $1::uuid`, uid).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return gid, err == nil, err
}

// UserUngrouped reports whether the user has no group at all: no vehicle row to hold a lease for, so no pod owns them and none can. (A group
// that another pod holds is not ungrouped.) The needs_reauth raise for such a user, found by the vehicle discovery job, has no owner to wait for.
func (s *Store) UserUngrouped(ctx context.Context, uid string) (bool, error) {
	var gid string
	err := s.db.QueryRowContext(ctx, userGroups+` SELECT gid FROM grp WHERE user_id = $1::uuid`, uid).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}
