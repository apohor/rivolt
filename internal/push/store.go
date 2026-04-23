// Package push handles Web Push subscriptions and notification delivery
// for self-hosted Rivolt installs. It persists browser push subscriptions
// in the same SQLite database the rest of the app uses and fans out
// notifications on "charging done", "plug-in reminder" and "anomaly" events.
package push

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS push_subscriptions (
    endpoint             TEXT PRIMARY KEY,
    p256dh               TEXT NOT NULL,
    auth                 TEXT NOT NULL,
    on_charging_done     INTEGER NOT NULL DEFAULT 1,
    on_plug_in_reminder  INTEGER NOT NULL DEFAULT 1,
    on_anomaly           INTEGER NOT NULL DEFAULT 1,
    user_agent           TEXT,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);

-- VAPID keypair is persisted in a one-row table so self-hosted installs
-- get a stable identity across restarts without the operator having to
-- manage env vars. Env vars, if present, still win at load time.
CREATE TABLE IF NOT EXISTS push_vapid (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    public_key  TEXT NOT NULL,
    private_key TEXT NOT NULL,
    subject     TEXT NOT NULL
);
`

// Subscription is a stored browser push subscription.
type Subscription struct {
	Endpoint         string
	P256dh           string
	Auth             string
	OnChargingDone   bool
	OnPlugInReminder bool
	OnAnomaly        bool
	UserAgent        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Preferences captures the per-subscription event filters.
type Preferences struct {
	OnChargingDone   bool `json:"on_charging_done"`
	OnPlugInReminder bool `json:"on_plug_in_reminder"`
	OnAnomaly        bool `json:"on_anomaly"`
}

// VAPID is the server identity used to sign outbound push requests.
type VAPID struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

// Store wraps access to push_subscriptions + push_vapid.
type Store struct{ db *sql.DB }

// OpenStore opens the push store at path. Safe to point at the same file
// other internal/* packages use; tables don't collide.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Upsert inserts-or-updates a subscription keyed on endpoint. The endpoint
// URL is effectively the browser's stable identifier for this (origin,
// installation) pair; reusing it lets a browser that re-subscribes — say
// after a permission toggle — overwrite the old row instead of piling up
// orphans.
func (s *Store) Upsert(ctx context.Context, sub Subscription) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (
			endpoint, p256dh, auth,
			on_charging_done, on_plug_in_reminder, on_anomaly,
			user_agent, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET
			p256dh              = excluded.p256dh,
			auth                = excluded.auth,
			on_charging_done    = excluded.on_charging_done,
			on_plug_in_reminder = excluded.on_plug_in_reminder,
			on_anomaly          = excluded.on_anomaly,
			user_agent          = excluded.user_agent,
			updated_at          = excluded.updated_at`,
		sub.Endpoint, sub.P256dh, sub.Auth,
		boolInt(sub.OnChargingDone), boolInt(sub.OnPlugInReminder), boolInt(sub.OnAnomaly),
		sub.UserAgent, now, now,
	)
	return err
}

// Delete removes a subscription. No-op if absent.
func (s *Store) Delete(ctx context.Context, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}

// List returns every subscription. Order is stable (oldest first) so the
// caller can reason about fan-out timing if it cares.
func (s *Store) List(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT endpoint, p256dh, auth,
		       on_charging_done, on_plug_in_reminder, on_anomaly,
		       COALESCE(user_agent, ''), created_at, updated_at
		FROM push_subscriptions
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var (
			s                     Subscription
			charge, plug, anomaly int
			created, updated      int64
		)
		if err := rows.Scan(&s.Endpoint, &s.P256dh, &s.Auth,
			&charge, &plug, &anomaly, &s.UserAgent, &created, &updated); err != nil {
			return nil, err
		}
		s.OnChargingDone = charge != 0
		s.OnPlugInReminder = plug != 0
		s.OnAnomaly = anomaly != 0
		s.CreatedAt = time.Unix(created, 0)
		s.UpdatedAt = time.Unix(updated, 0)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Count returns the number of stored subscriptions. Handy for the
// settings UI status line.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subscriptions`).Scan(&n)
	return n, err
}

// GetVAPID returns the persisted VAPID keypair or (nil, nil) if none is
// stored yet.
func (s *Store) GetVAPID(ctx context.Context) (*VAPID, error) {
	var v VAPID
	err := s.db.QueryRowContext(ctx,
		`SELECT public_key, private_key, subject FROM push_vapid WHERE id = 1`,
	).Scan(&v.PublicKey, &v.PrivateKey, &v.Subject)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SaveVAPID persists the keypair. The table is pinned to a single row
// (id=1) so this is effectively a replace.
func (s *Store) SaveVAPID(ctx context.Context, v VAPID) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_vapid (id, public_key, private_key, subject)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			public_key  = excluded.public_key,
			private_key = excluded.private_key,
			subject     = excluded.subject`,
		v.PublicKey, v.PrivateKey, v.Subject,
	)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
