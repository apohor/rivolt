package appleid

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LinkIntentTTL is how long a started link stays open: the time to get through Apple's sign-in page.
const LinkIntentTTL = 10 * time.Minute

// linkIntentKeep is how long retention keeps a spent or expired intent row before sweeping it.
const linkIntentKeep = 24 * time.Hour

// Hash is the sha256 the intent table stores for an OAuth state or a session id: the callback and the sign-out hook
// look rows up by it, and the table never holds the raw values.
func Hash(raw string) []byte {
	h := sha256.Sum256([]byte(raw))
	return h[:]
}

// LinkIntent is a consumed link request: who started it and from which session.
type LinkIntent struct {
	UserID      uuid.UUID
	SessionHash []byte
}

// CreateLinkIntent records that userID, signed in with the session whose hash is sessionHash, started linking an
// Apple identity with the OAuth state whose hash is stateHash. A user has at most one open intent: starting again
// replaces the earlier one.
func (s *Store) CreateLinkIntent(ctx context.Context, userID uuid.UUID, stateHash, sessionHash []byte, now time.Time) error {
	if userID == uuid.Nil || len(stateHash) == 0 || len(sessionHash) == 0 {
		return errors.New("appleid: link intent needs a user, a state and a session")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO apple_link_intents (state_hash, user_id, session_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id) DO UPDATE
		   SET state_hash = EXCLUDED.state_hash, session_hash = EXCLUDED.session_hash,
		       created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at`,
		stateHash, userID, sessionHash, now, now.Add(LinkIntentTTL))
	if err != nil {
		return fmt.Errorf("appleid: create link intent: %w", err)
	}
	return nil
}

// ConsumeLinkIntent takes the intent for stateHash, once: the row is deleted whether or not it is still good. ok is
// false when there is none, or it has expired.
func (s *Store) ConsumeLinkIntent(ctx context.Context, stateHash []byte, now time.Time) (LinkIntent, bool, error) {
	var (
		in      LinkIntent
		expires time.Time
	)
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM apple_link_intents WHERE state_hash = $1
		RETURNING user_id, session_hash, expires_at`, stateHash).Scan(&in.UserID, &in.SessionHash, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return LinkIntent{}, false, nil
	}
	if err != nil {
		return LinkIntent{}, false, fmt.Errorf("appleid: consume link intent: %w", err)
	}
	if !now.Before(expires) {
		return LinkIntent{}, false, nil
	}
	return in, true, nil
}

// DropSessionLinkIntents drops every open intent started from the session whose hash is sessionHash (sign-out).
func (s *Store) DropSessionLinkIntents(ctx context.Context, sessionHash []byte) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM apple_link_intents WHERE session_hash = $1`, sessionHash); err != nil {
		return fmt.Errorf("appleid: drop link intents: %w", err)
	}
	return nil
}

// PurgeLinkIntents is the retention sweep: intents created more than a day ago, spent or not.
func (s *Store) PurgeLinkIntents(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM apple_link_intents WHERE created_at < now() - $1::interval`, fmt.Sprintf("%d seconds", int(linkIntentKeep.Seconds())))
	if err != nil {
		return 0, fmt.Errorf("appleid: purge link intents: %w", err)
	}
	return res.RowsAffected()
}
