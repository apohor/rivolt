package leases

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/fence"
)

// HeldUser is a user this pod owns the Rivian session of, with the token of the group lease that makes it so.
type HeldUser struct {
	User  uuid.UUID
	Token fence.Token
}

// HeldGroupUsers is every user in the groups this pod holds (as of its last renew), with that group's fence token. Single-owner M4-2b: the owner's
// vehicle discovery looks up the ones with no vehicle yet and writes its answer under the token. Read only.
func (s *Store) HeldGroupUsers(ctx context.Context) ([]HeldUser, error) {
	toks := s.GroupTokens()
	if len(toks) == 0 {
		return nil, nil
	}
	groups := make([]string, len(toks))
	epoch := make(map[string]int64, len(toks))
	for i, t := range toks {
		groups[i] = t.Group
		epoch[t.Group] = t.Epoch
	}
	rows, err := s.db.QueryContext(ctx, userGroups+` SELECT user_id, gid FROM grp WHERE gid = ANY($1::text[]) ORDER BY user_id`, groups)
	if err != nil {
		return nil, fmt.Errorf("held group users: %w", err)
	}
	defer rows.Close()
	var out []HeldUser
	for rows.Next() {
		var u uuid.UUID
		var gid string
		if err := rows.Scan(&u, &gid); err != nil {
			return nil, err
		}
		out = append(out, HeldUser{User: u, Token: fence.Token{Group: gid, Pod: s.podID, Epoch: epoch[gid]}})
	}
	return out, rows.Err()
}
