package settings

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/apohor/rivolt/internal/rivian"
)

// keyRivianSession holds the JSON-encoded rivian.Session. Tokens live
// in the same KV row rather than separate keys so a single Set is
// atomic w.r.t. login/logout — we never want a half-applied session
// on disk.
//
// This row contains long-lived bearer tokens. The SQLite database is
// expected to be stored on a trusted, per-user volume; encrypting the
// row would be security theatre until we move to a shared-host
// deployment model.
const keyRivianSession = "rivian.session"

// LoadRivianSession returns the persisted Rivian session, if any. A
// missing key is not an error; callers get the zero value.
func LoadRivianSession(ctx context.Context, s *Store) (rivian.Session, error) {
	if s == nil {
		return rivian.Session{}, nil
	}
	all, err := s.GetAll(ctx)
	if err != nil {
		return rivian.Session{}, fmt.Errorf("load rivian session: %w", err)
	}
	raw, ok := all[keyRivianSession]
	if !ok || raw == "" {
		return rivian.Session{}, nil
	}
	var sess rivian.Session
	if err := json.Unmarshal([]byte(raw), &sess); err != nil {
		// Corrupt row: treat as no session. We don't fail startup for
		// a bad settings value; the operator can log in again to
		// overwrite it.
		return rivian.Session{}, nil
	}
	return sess, nil
}

// SaveRivianSession persists (or clears, on zero value) the session.
// An empty UserSessionToken is the sentinel for "no session"; we write
// an empty string in that case so the row exists but signals logout.
func SaveRivianSession(ctx context.Context, s *Store, sess rivian.Session) error {
	if s == nil {
		return nil
	}
	if sess.UserSessionToken == "" {
		return s.Set(ctx, keyRivianSession, "")
	}
	buf, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal rivian session: %w", err)
	}
	return s.Set(ctx, keyRivianSession, string(buf))
}
