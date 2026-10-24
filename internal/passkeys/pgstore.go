package passkeys

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// PGStore keeps passkeys in Postgres. It works on the system pool: sign-in runs before there is a user in the request, and
// the tables carry tenant isolation for the per-user pool like every table with a user_id.
type PGStore struct{ db *sql.DB }

// NewPGStore builds the store.
func NewPGStore(db *sql.DB) *PGStore { return &PGStore{db: db} }

var _ Store = (*PGStore)(nil)

func (s *PGStore) UserHandle(ctx context.Context, uid uuid.UUID) ([]byte, error) {
	var h []byte
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO webauthn_users (user_id, handle) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET user_id = webauthn_users.user_id
		RETURNING handle`, uid, NewHandle()).Scan(&h)
	if err != nil {
		return nil, fmt.Errorf("passkeys: user handle: %w", err)
	}
	return h, nil
}

func (s *PGStore) UserByHandle(ctx context.Context, handle []byte) (uuid.UUID, bool, error) {
	var uid uuid.UUID
	err := s.db.QueryRowContext(ctx, `SELECT user_id FROM webauthn_users WHERE handle = $1`, handle).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("passkeys: handle lookup: %w", err)
	}
	return uid, true, nil
}

const credColumns = `id, user_id, credential_id, public_key, sign_count, transports, aaguid, backup_eligible, backup_state, label, created_at, last_used_at`

func scanCredential(row interface{ Scan(...any) error }) (Credential, error) {
	var c Credential
	var sc int64
	var last sql.NullTime
	var transports string
	if err := row.Scan(&c.ID, &c.UserID, &c.CredentialID, &c.PublicKey, &sc, &transports, &c.AAGUID,
		&c.BackupEligible, &c.BackupState, &c.Label, &c.CreatedAt, &last); err != nil {
		return Credential{}, err
	}
	c.SignCount = uint32(sc)
	if transports != "" {
		c.Transports = strings.Split(transports, ",")
	}
	if last.Valid {
		t := last.Time
		c.LastUsedAt = &t
	}
	return c, nil
}

func (s *PGStore) Credentials(ctx context.Context, uid uuid.UUID) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+credColumns+` FROM webauthn_credentials WHERE user_id = $1 ORDER BY created_at`, uid)
	if err != nil {
		return nil, fmt.Errorf("passkeys: list: %w", err)
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("passkeys: list: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PGStore) CredentialByID(ctx context.Context, credentialID []byte) (Credential, bool, error) {
	c, err := scanCredential(s.db.QueryRowContext(ctx, `SELECT `+credColumns+` FROM webauthn_credentials WHERE credential_id = $1`, credentialID))
	if errors.Is(err, sql.ErrNoRows) {
		return Credential{}, false, nil
	}
	if err != nil {
		return Credential{}, false, fmt.Errorf("passkeys: credential lookup: %w", err)
	}
	return c, true, nil
}

func (s *PGStore) Add(ctx context.Context, c Credential) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webauthn_credentials (id, user_id, credential_id, public_key, sign_count, transports, aaguid,
		                                  backup_eligible, backup_state, label, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		c.ID, c.UserID, c.CredentialID, c.PublicKey, int64(c.SignCount), strings.Join(c.Transports, ","), c.AAGUID,
		c.BackupEligible, c.BackupState, c.Label, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("passkeys: add: %w", err)
	}
	return nil
}

func (s *PGStore) RecordUse(ctx context.Context, credentialID []byte, signCount uint32, backupState bool, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE webauthn_credentials SET sign_count = $2, backup_state = $3, last_used_at = $4 WHERE credential_id = $1`,
		credentialID, int64(signCount), backupState, at)
	if err != nil {
		return fmt.Errorf("passkeys: record use: %w", err)
	}
	return nil
}

func (s *PGStore) Rename(ctx context.Context, uid, id uuid.UUID, label string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE webauthn_credentials SET label = $3 WHERE id = $1 AND user_id = $2`, id, uid, label)
	if err != nil {
		return false, fmt.Errorf("passkeys: rename: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *PGStore) Delete(ctx context.Context, uid, id uuid.UUID) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE id = $1 AND user_id = $2`, id, uid)
	if err != nil {
		return false, fmt.Errorf("passkeys: delete: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *PGStore) SaveCeremony(ctx context.Context, c Ceremony) error {
	var uid any
	if c.UserID != uuid.Nil {
		uid = c.UserID
	}
	var hc any
	if c.HydraChallenge != "" {
		hc = c.HydraChallenge
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webauthn_ceremonies (id, purpose, user_id, hydra_challenge, session, expires_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		c.ID, c.Purpose, uid, hc, c.Session, c.ExpiresAt)
	if err != nil {
		return fmt.Errorf("passkeys: save ceremony: %w", err)
	}
	return nil
}

func (s *PGStore) TakeCeremony(ctx context.Context, id uuid.UUID, purpose string, now time.Time) (Ceremony, bool, error) {
	var c Ceremony
	var uid uuid.NullUUID
	var hc sql.NullString
	err := s.db.QueryRowContext(ctx, `
		DELETE FROM webauthn_ceremonies WHERE id = $1 AND purpose = $2 AND expires_at > $3
		RETURNING id, purpose, user_id, hydra_challenge, session, expires_at`, id, purpose, now).
		Scan(&c.ID, &c.Purpose, &uid, &hc, &c.Session, &c.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Ceremony{}, false, nil
	}
	if err != nil {
		return Ceremony{}, false, fmt.Errorf("passkeys: take ceremony: %w", err)
	}
	if uid.Valid {
		c.UserID = uid.UUID
	}
	c.HydraChallenge = hc.String
	return c, true, nil
}

func (s *PGStore) PurgeCeremonies(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE expires_at < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("passkeys: purge ceremonies: %w", err)
	}
	return res.RowsAffected()
}
