package secrets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/google/uuid"
)

// rivianSessionName is the user_secrets.name used for the sealed
// rivian.Session JSON blob. Stable across releases — the name is
// part of the storage contract.
const rivianSessionName = "rivian.session"

// LoadRivianSession returns the persisted Rivian session for a
// user, or the zero value when none is stored. A corrupt /
// un-openable blob also returns zero rather than an error:
// matches the previous settings-backed behaviour where an
// operator could "recover" a bad row by logging in again, and
// means a failed KEK rotation doesn't brick every user at once.
// The caller gets an error log-worthy only when the DB itself
// misbehaves.
func LoadRivianSession(ctx context.Context, s *Store, userID uuid.UUID) (rivian.Session, error) {
	if s == nil {
		return rivian.Session{}, nil
	}
	plaintext, err := s.Get(ctx, userID, rivianSessionName)
	if errors.Is(err, ErrNotFound) {
		return rivian.Session{}, nil
	}
	if err != nil {
		// Sealer failure is treated like a missing row so a
		// single-user crypto problem doesn't cascade into a
		// startup failure. The caller logs it.
		return rivian.Session{}, fmt.Errorf("load rivian session: %w", err)
	}
	var sess rivian.Session
	if err := json.Unmarshal(plaintext, &sess); err != nil {
		// Corrupt JSON is recovered the same way the old
		// settings path did: treat as empty, operator re-logs
		// in to overwrite.
		return rivian.Session{}, nil
	}
	return sess, nil
}

// SaveRivianSession persists or clears the session. An empty
// UserSessionToken is the sentinel for "logged out"; we delete
// the row rather than store an empty sealed blob, because a
// missing row is cheaper to read and removes the only place
// where the user has stale token material on disk.
func SaveRivianSession(ctx context.Context, s *Store, userID uuid.UUID, sess rivian.Session) error {
	if s == nil {
		return nil
	}
	if sess.UserSessionToken == "" {
		return s.Delete(ctx, userID, rivianSessionName)
	}
	buf, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal rivian session: %w", err)
	}
	return s.Put(ctx, userID, rivianSessionName, buf)
}
