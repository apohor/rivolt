package leases

import (
	"context"
	"database/sql"
	"errors"

	"github.com/apohor/rivolt/internal/fence"
)

// FenceUser is Fence for a user rather than a vehicle (the receipts sync is per user): the token
// of the user's group when this pod holds it. One query maps the user to their group (the same
// userGroups CTE the leases use); the rest is the in-memory state Fence reads. A user with no
// vehicle has no group, so no owner, and is Lost in lead mode.
func (s *Store) FenceUser(ctx context.Context, uid string) (fence.Token, fence.State, error) {
	s.groupMu.Lock()
	lead := s.lead
	s.groupMu.Unlock()
	if !lead {
		return fence.Token{}, fence.Off, nil
	}
	var gid string
	err := s.db.QueryRowContext(ctx, userGroups+` SELECT gid FROM grp WHERE user_id = $1::uuid`, uid).Scan(&gid)
	if errors.Is(err, sql.ErrNoRows) {
		return fence.Token{}, fence.Lost, nil
	}
	if err != nil {
		return fence.Token{}, fence.Lost, err
	}
	s.groupMu.Lock()
	defer s.groupMu.Unlock()
	tok, st := s.tokenLocked(gid)
	return tok, st, nil
}
