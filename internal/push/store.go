// Package push handles Web Push subscriptions and notification delivery.
// Subscriptions and the singleton VAPID keypair live in the shared
// Postgres database.
package push

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

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

// Store wraps access to push_subscriptions + push_vapid. Scoped to a
// single user_id; the VAPID keypair is global (push_vapid.id = 1).
type Store struct {
	db     *sql.DB
	userID uuid.UUID
}

// OpenStore binds a pooled connection to a user_id. The pool is owned
// by the caller.
func OpenStore(db *sql.DB, userID uuid.UUID) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("push: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("push: userID is zero")
	}
	return &Store{db: db, userID: userID}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Upsert inserts or updates a subscription keyed on (user_id, endpoint).
func (s *Store) Upsert(ctx context.Context, sub Subscription) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (
			user_id, endpoint, p256dh, auth,
			on_charging_done, on_plug_in_reminder, on_anomaly,
			user_agent
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (user_id, endpoint) DO UPDATE SET
			p256dh              = EXCLUDED.p256dh,
			auth                = EXCLUDED.auth,
			on_charging_done    = EXCLUDED.on_charging_done,
			on_plug_in_reminder = EXCLUDED.on_plug_in_reminder,
			on_anomaly          = EXCLUDED.on_anomaly,
			user_agent          = EXCLUDED.user_agent,
			updated_at          = NOW()`,
		s.userID, sub.Endpoint, sub.P256dh, sub.Auth,
		sub.OnChargingDone, sub.OnPlugInReminder, sub.OnAnomaly,
		sub.UserAgent,
	)
	return err
}

// Delete removes a subscription. No-op if absent.
func (s *Store) Delete(ctx context.Context, endpoint string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2`,
		s.userID, endpoint)
	return err
}

// List returns every subscription for this user, oldest first.
func (s *Store) List(ctx context.Context) ([]Subscription, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT endpoint, p256dh, auth,
		       on_charging_done, on_plug_in_reminder, on_anomaly,
		       COALESCE(user_agent, ''), created_at, updated_at
		FROM push_subscriptions
		WHERE user_id = $1
		ORDER BY created_at ASC`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var v Subscription
		if err := rows.Scan(&v.Endpoint, &v.P256dh, &v.Auth,
			&v.OnChargingDone, &v.OnPlugInReminder, &v.OnAnomaly,
			&v.UserAgent, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Count returns the number of stored subscriptions for this user.
func (s *Store) Count(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM push_subscriptions WHERE user_id = $1`,
		s.userID).Scan(&n)
	return n, err
}

// GetVAPID returns the persisted VAPID keypair or (nil, nil) if none is
// stored yet. The keypair is shared across all users of this install.
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

// SaveVAPID persists the keypair at id=1 (singleton row).
func (s *Store) SaveVAPID(ctx context.Context, v VAPID) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_vapid (id, public_key, private_key, subject)
		VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET
			public_key  = EXCLUDED.public_key,
			private_key = EXCLUDED.private_key,
			subject     = EXCLUDED.subject`,
		v.PublicKey, v.PrivateKey, v.Subject,
	)
	return err
}
