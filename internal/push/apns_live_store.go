package push

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// The charging Live Activity's registrations (push_apns_activities). Nothing here
// sends; the sender reads these rows behind RIVOLT_APNS_LIVE.

// ErrLiveOff is "this iPhone's Live Activity switch is off".
var ErrLiveOff = errors.New("push: live charging is off for this device")

// ActivityTTL is the longest a registration lives: an activity runs 8 hours (12 on
// screen), so a row older than this is stale whatever the app said.
const ActivityTTL = 12 * time.Hour

// maxActivitiesPerUser caps the rows one account can hold (one per charging truck
// is plenty); the oldest go first.
const maxActivitiesPerUser = 5

// activityTokenRE is ActivityKit's per-activity push token as the app reports it:
// hex, variable length.
var activityTokenRE = regexp.MustCompile(`^[0-9a-fA-F]{32,400}$`)

// ValidActivityToken reports whether t has the shape of an activity push token.
func ValidActivityToken(t string) bool { return activityTokenRE.MatchString(t) }

// APNsLive reports the Live Activity switch of one of the user's devices; ok is
// false when the token is not the user's.
func (s *Store) APNsLive(ctx context.Context, token string) (on, ok bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT on_charging_live FROM push_apns_devices WHERE user_id = $1 AND token = $2`,
		s.userID, token).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return on, err == nil, err
}

// SetAPNsLive sets the Live Activity switch of one of the user's devices. Turning
// it off also forgets the device's registered activities. Reports whether the
// token is the user's.
func (s *Store) SetAPNsLive(ctx context.Context, token string, on bool) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var id uuid.UUID
	err = tx.QueryRowContext(ctx, `
		UPDATE push_apns_devices SET on_charging_live = $3
		 WHERE user_id = $1 AND token = $2 RETURNING id`, s.userID, token, on).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !on {
		if _, err := tx.ExecContext(ctx, `DELETE FROM push_apns_activities WHERE device_id = $1`, id); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// RegisterActivity records a running Live Activity's push token for one of the
// user's devices and vehicles. ErrNotFound when the device or vehicle is not the
// user's; ErrLiveOff when the device's switch is off. Idempotent: the same token
// again re-points it at the device and vehicle but keeps its start time.
func (s *Store) RegisterActivity(ctx context.Context, deviceToken, activityToken string, vehicleID uuid.UUID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var (
		deviceID uuid.UUID
		live     bool
	)
	err = tx.QueryRowContext(ctx,
		`SELECT id, on_charging_live FROM push_apns_devices WHERE user_id = $1 AND token = $2`,
		s.userID, deviceToken).Scan(&deviceID, &live)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !live {
		return ErrLiveOff
	}
	var one int
	err = tx.QueryRowContext(ctx, `SELECT 1 FROM vehicles WHERE id = $1 AND user_id = $2`, vehicleID, s.userID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// An activity token is one running activity: it belongs to whoever registered
	// it last (a sign-out that never reached the server).
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_apns_activities WHERE token = $1 AND user_id <> $2`, activityToken, s.userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO push_apns_activities (id, user_id, device_id, vehicle_id, token)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (user_id, token) DO UPDATE SET device_id = EXCLUDED.device_id, vehicle_id = EXCLUDED.vehicle_id`,
		uuid.New(), s.userID, deviceID, vehicleID, activityToken); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM push_apns_activities WHERE id IN (
			SELECT id FROM push_apns_activities WHERE user_id = $1
			 ORDER BY started_at DESC, id OFFSET $2)`, s.userID, maxActivitiesPerUser); err != nil {
		return err
	}
	return tx.Commit()
}

// EndActivity forgets one of the user's activity tokens. No-op if absent.
func (s *Store) EndActivity(ctx context.Context, activityToken string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM push_apns_activities WHERE user_id = $1 AND token = $2`, s.userID, activityToken)
	return err
}

// ActivityCount is how many activities the user has registered (tests, and the
// export's count if it ever wants one).
func (s *Store) ActivityCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM push_apns_activities WHERE user_id = $1`, s.userID).Scan(&n)
	return n, err
}

// PurgeActivities deletes every registration older than ActivityTTL (all users).
// Run hourly.
func PurgeActivities(ctx context.Context, db *sql.DB) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM push_apns_activities WHERE started_at < now() - make_interval(secs => $1)`, ActivityTTL.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Activity is a registered running Live Activity, with what a send needs.
type Activity struct {
	ID       uuid.UUID
	Token    string
	Env      string
	BundleID string
}

// ListLiveActivities returns the user's running activities on one vehicle whose
// device has the Live Activity switch on and a live (not revoked, not expired) app
// session, and which are not older than ActivityTTL.
func (s *Store) ListLiveActivities(ctx context.Context, vehicleID uuid.UUID) ([]Activity, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.token, d.env, d.bundle_id
		  FROM push_apns_activities a
		  JOIN push_apns_devices d ON d.id = a.device_id
		  JOIN sessions se ON se.id = d.session_id
		 WHERE a.user_id = $1 AND a.vehicle_id = $2
		   AND d.on_charging_live
		   AND se.revoked_at IS NULL AND se.expires_at > now()
		   AND a.started_at > now() - make_interval(secs => $3)
		 ORDER BY a.started_at`, s.userID, vehicleID, ActivityTTL.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.Token, &a.Env, &a.BundleID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// claimActivityUpdate reserves a routine update for one activity: true when none
// was sent in the last minInterval (and records this one), false otherwise.
// Atomic, so two senders cannot both send inside the interval.
func (s *Store) claimActivityUpdate(ctx context.Context, id uuid.UUID, minInterval time.Duration) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_apns_activities SET last_sent_at = now()
		 WHERE user_id = $1 AND id = $2
		   AND (last_sent_at IS NULL OR last_sent_at <= now() - make_interval(secs => $3))`,
		s.userID, id, minInterval.Seconds())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (s *Store) deleteActivityByID(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_apns_activities WHERE user_id = $1 AND id = $2`, s.userID, id)
	return err
}
