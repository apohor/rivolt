// Package push handles Web Push subscriptions and notification delivery.
// Subscriptions and the singleton VAPID keypair live in the shared
// Postgres database.
package push

import (
	"context"
	"database/sql"
	"errors"
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
	OnSoftwareUpdate bool
	// OnMilestones is the opt-in push for milestone moments (default off).
	OnMilestones bool
	// OnLeftOpen is the opt-in push for a door, the frunk, the liftgate, the tailgate or the tonneau left open (default off).
	OnLeftOpen bool
	// OnTireLow is the opt-in push for a tire the truck itself reports low (default off).
	OnTireLow bool
	UserAgent string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Preferences captures the per-subscription event filters.
type Preferences struct {
	OnChargingDone   bool `json:"on_charging_done"`
	OnPlugInReminder bool `json:"on_plug_in_reminder"`
	OnAnomaly        bool `json:"on_anomaly"`
	OnSoftwareUpdate bool `json:"on_software_update"`
	OnMilestones     bool `json:"on_milestones"`
	OnLeftOpen       bool `json:"on_left_open"`
	OnTireLow        bool `json:"on_tire_low"`
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
	// sys is the owner-role pool, for the few statements that must see other users' rows: a device or activity token is one
	// install, so registering it checks and clears another account's registration of it. Under row-level security the request
	// pool shows this user's rows only, so that check would find nothing and silently allow a duplicate. nil means db (no
	// request pool). Every statement run on it still names s.userID explicitly.
	sys *sql.DB
}

// crossUser is the pool for statements that must span users (see Store.sys).
func (s *Store) crossUser() *sql.DB {
	if s.sys != nil {
		return s.sys
	}
	return s.db
}

// OpenStore binds a pooled connection to a user_id. The pool is owned
// by the caller.
func OpenStore(db *sql.DB, userID uuid.UUID) (*Store, error) {
	return openStore(db, nil, userID)
}

// openStore is OpenStore with the system pool for the cross-user statements.
func openStore(db, sys *sql.DB, userID uuid.UUID) (*Store, error) {
	if db == nil {
		return nil, fmt.Errorf("push: db is nil")
	}
	if userID == uuid.Nil {
		return nil, fmt.Errorf("push: userID is zero")
	}
	return &Store{db: db, sys: sys, userID: userID}, nil
}

// Close is a no-op; the pool is managed by main.
func (s *Store) Close() error { return nil }

// Upsert inserts or updates a subscription keyed on (user_id, endpoint). It
// never sets the two opt-in switches (plug-in reminder, milestones) on: a new
// row starts with both off and a re-subscribe leaves what is stored. Only
// SetOptIns, reached from the Settings screen's own control, turns them on, so
// a stale tab or an old pod posting its local defaults cannot opt anyone in.
func (s *Store) Upsert(ctx context.Context, sub Subscription) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO push_subscriptions (
			user_id, endpoint, p256dh, auth,
			on_charging_done, on_plug_in_reminder, on_anomaly, on_software_update, on_milestones,
			user_agent
		) VALUES ($1,$2,$3,$4,$5,FALSE,$6,$7,FALSE,$8)
		ON CONFLICT (user_id, endpoint) DO UPDATE SET
			p256dh              = EXCLUDED.p256dh,
			auth                = EXCLUDED.auth,
			on_charging_done    = EXCLUDED.on_charging_done,
			on_anomaly          = EXCLUDED.on_anomaly,
			on_software_update  = EXCLUDED.on_software_update,
			user_agent          = EXCLUDED.user_agent,
			updated_at          = NOW()`,
		s.userID, sub.Endpoint, sub.P256dh, sub.Auth,
		sub.OnChargingDone, sub.OnAnomaly, sub.OnSoftwareUpdate,
		sub.UserAgent,
	)
	return err
}

// SetOptIns sets the plug-in reminder and/or milestones switch on one of the
// user's subscriptions (nil leaves a switch alone) and reports whether the
// endpoint is the user's.
func (s *Store) SetOptIns(ctx context.Context, endpoint string, plugIn, milestones *bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_subscriptions
		   SET on_plug_in_reminder = COALESCE($3, on_plug_in_reminder),
		       on_milestones       = COALESCE($4, on_milestones),
		       updated_at          = NOW()
		 WHERE user_id = $1 AND endpoint = $2`, s.userID, endpoint, plugIn, milestones)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetLeftOpen sets the left-open switch (off until the user's own control turns it on) on one of the user's
// subscriptions. Reports whether the endpoint is the user's.
func (s *Store) SetLeftOpen(ctx context.Context, endpoint string, on bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_subscriptions SET on_left_open = $3, updated_at = NOW()
		 WHERE user_id = $1 AND endpoint = $2`, s.userID, endpoint, on)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetTireLow sets the tire-low switch (off until the user's own control turns it on) on one of the user's
// subscriptions. Reports whether the endpoint is the user's.
func (s *Store) SetTireLow(ctx context.Context, endpoint string, on bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_subscriptions SET on_tire_low = $3, updated_at = NOW()
		 WHERE user_id = $1 AND endpoint = $2`, s.userID, endpoint, on)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
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
		       on_charging_done, on_plug_in_reminder, on_anomaly, on_software_update, on_milestones, on_left_open, on_tire_low,
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
			&v.OnChargingDone, &v.OnPlugInReminder, &v.OnAnomaly, &v.OnSoftwareUpdate, &v.OnMilestones, &v.OnLeftOpen, &v.OnTireLow,
			&v.UserAgent, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Get returns the user's subscription for endpoint, or (nil, nil) when the
// endpoint is not one of theirs.
func (s *Store) Get(ctx context.Context, endpoint string) (*Subscription, error) {
	var v Subscription
	err := s.db.QueryRowContext(ctx, `
		SELECT endpoint, p256dh, auth,
		       on_charging_done, on_plug_in_reminder, on_anomaly, on_software_update, on_milestones, on_left_open, on_tire_low,
		       COALESCE(user_agent, ''), created_at, updated_at
		FROM push_subscriptions
		WHERE user_id = $1 AND endpoint = $2`, s.userID, endpoint).Scan(&v.Endpoint, &v.P256dh, &v.Auth,
		&v.OnChargingDone, &v.OnPlugInReminder, &v.OnAnomaly, &v.OnSoftwareUpdate, &v.OnMilestones, &v.OnLeftOpen, &v.OnTireLow,
		&v.UserAgent, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// SwitchOffAll turns one opt-in kind (plug_in_reminder, milestones, left_open or tire_low) off on
// every one of the user's subscriptions and reports how many changed.
func (s *Store) SwitchOffAll(ctx context.Context, kind string) (int64, error) {
	var col string
	switch kind {
	case "plug_in_reminder":
		col = "on_plug_in_reminder"
	case "milestones":
		col = "on_milestones"
	case "left_open":
		col = "on_left_open"
	case "tire_low":
		col = "on_tire_low"
	default:
		return 0, fmt.Errorf("push: unknown kind %q", kind)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE push_subscriptions SET `+col+` = FALSE, updated_at = NOW() WHERE user_id = $1 AND `+col, s.userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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

// NewServerStore returns a *Store usable only for VAPID Get/Save.
// Subscription ops will fail (userID is zero); that's by design —
// VAPID is the only push state that's truly server-wide.
func NewServerStore(db *sql.DB) *Store {
	return &Store{db: db}
}
