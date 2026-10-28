package secrets

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
	"github.com/google/uuid"
)

// rivianSessionName is the user_secrets.name used for the sealed
// rivian.Session JSON blob. Stable across releases — the name is
// part of the storage contract.
const rivianSessionName = "rivian.session"

// rivianPendingMFAName is the user_secrets.name for the sealed
// in-flight OTP challenge. It lets whichever pod handles the OTP
// submission rehydrate a challenge that the password leg opened on a
// different pod. Cleared on success or logout.
const rivianPendingMFAName = "rivian.pending-mfa"

// pendingMFATTL bounds how long a persisted OTP challenge stays
// usable. Rivian expires the emailed code on its side; this just
// keeps a stale cross-pod challenge from lingering in storage.
const pendingMFATTL = 15 * time.Minute

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
		return s.DeleteTx(ctx, userID, rivianSessionName, func(ctx context.Context, tx *sql.Tx) error {
			return setSignedIn(ctx, tx, userID, time.Time{})
		})
	}
	buf, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("marshal rivian session: %w", err)
	}
	return s.PutTx(ctx, userID, rivianSessionName, buf, func(ctx context.Context, tx *sql.Tx) error {
		return setSignedIn(ctx, tx, userID, sess.AuthenticatedAt)
	})
}

// RivianSignedInUnknown is what users.rivian_signed_in_at holds for a stored session that has
// no sign-in time (1970-01-01): recorded, so the expiry sweep does not open it again.
var RivianSignedInUnknown = time.Unix(0, 0).UTC()

// setSignedIn records the sign-in time beside the session, in the session's transaction. A zero
// time clears it (sign-out, or a session with no time: the expiry sweep fills the column once).
func setSignedIn(ctx context.Context, tx *sql.Tx, userID uuid.UUID, at time.Time) error {
	var v any
	if !at.IsZero() {
		v = at.UTC()
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET rivian_signed_in_at = $2 WHERE id = $1`, userID, v); err != nil {
		return fmt.Errorf("record rivian sign-in time: %w", err)
	}
	return nil
}

// SavePendingMFA persists an in-flight OTP challenge so a peer pod can
// finish the second leg. An empty OTPToken clears the row instead.
func SavePendingMFA(ctx context.Context, s *Store, userID uuid.UUID, p rivian.PendingMFA) error {
	if s == nil {
		return nil
	}
	if p.OTPToken == "" {
		return ClearPendingMFA(ctx, s, userID)
	}
	if p.IssuedAt.IsZero() {
		p.IssuedAt = time.Now()
	}
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal pending mfa: %w", err)
	}
	return s.Put(ctx, userID, rivianPendingMFAName, buf)
}

// LoadPendingMFA returns a stored OTP challenge and true when one is
// present and within the TTL. A missing, corrupt, or expired row
// returns false, matching LoadRivianSession's recover-by-retry stance.
func LoadPendingMFA(ctx context.Context, s *Store, userID uuid.UUID) (rivian.PendingMFA, bool) {
	if s == nil {
		return rivian.PendingMFA{}, false
	}
	plaintext, err := s.Get(ctx, userID, rivianPendingMFAName)
	if err != nil {
		return rivian.PendingMFA{}, false
	}
	var p rivian.PendingMFA
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return rivian.PendingMFA{}, false
	}
	if p.OTPToken == "" || time.Since(p.IssuedAt) > pendingMFATTL {
		return rivian.PendingMFA{}, false
	}
	return p, true
}

// ClearPendingMFA removes any stored OTP challenge for the user.
func ClearPendingMFA(ctx context.Context, s *Store, userID uuid.UUID) error {
	if s == nil {
		return nil
	}
	return s.Delete(ctx, userID, rivianPendingMFAName)
}

// legacySessionFields are the two Rivian tokens older versions kept in the stored session
// (the access and refresh token Rivian's Login used to return). Nothing ever used them, and the
// current Login does not request them; a stored session written before that still holds them.
var legacySessionFields = []string{"access_token", "refresh_token"}

// ScrubLegacyRivianTokens removes the legacy access and refresh tokens from a user's stored
// session, if it still has them, without touching anything else and without changing the row's
// version (updated_at): it re-seals the same session minus those two fields, and only if nobody
// wrote the row since it was read, so a sign-in on another pod in between is never overwritten.
// scrubbed is false when there was nothing to remove or the row changed meanwhile.
func ScrubLegacyRivianTokens(ctx context.Context, s *Store, userID uuid.UUID) (scrubbed bool, err error) {
	if s == nil {
		return false, nil
	}
	plaintext, at, err := s.GetVersioned(ctx, userID, rivianSessionName)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(plaintext, &fields); err != nil {
		return false, nil // not ours to repair: LoadRivianSession treats it as empty
	}
	found := false
	for _, k := range legacySessionFields {
		if _, ok := fields[k]; ok {
			delete(fields, k)
			found = true
		}
	}
	if !found {
		return false, nil
	}
	clean, err := json.Marshal(fields)
	if err != nil {
		return false, fmt.Errorf("marshal rivian session: %w", err)
	}
	return s.ReplaceIfUnchanged(ctx, userID, rivianSessionName, clean, at)
}
