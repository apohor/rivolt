package push

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// APNsDevice is an iPhone app install registered for push (push_apns_devices).
// Token is the raw APNs device token; it is needed to send, is never logged and
// is not part of the data export (ExportRow).
type APNsDevice struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	Token     string
	Env       string
	BundleID  string
	Prefs     Preferences
	CreatedAt time.Time
	LastOKAt  *time.Time
	// InstallID is the random id the app keeps in its Keychain, sent on register. Only its hash is stored (install_hash);
	// the raw value is never kept, logged or exported. Empty from an older app.
	InstallID string
}

// ErrDeviceElsewhere is a register refused because the token is registered to another account whose session is live, and
// the caller did not prove it is the same install (the install id matches). Reason says which: "install_mismatch" or
// "install_missing" (a fixed list, safe as a metric label).
type ErrDeviceElsewhere struct{ Reason string }

func (e ErrDeviceElsewhere) Error() string { return "push: device is registered to another account" }

// installHash is what is stored of an install id: sha256, hex. Empty for no id.
func installHash(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	h := sha256.Sum256([]byte(id))
	return hex.EncodeToString(h[:])
}

// ErrNotFound is "no such notification (for this user, still fresh)".
var ErrNotFound = errors.New("push: not found")

// NotificationTTL is how long a notification's text is kept for the app to fetch.
const NotificationTTL = 24 * time.Hour

// UpsertAPNsDevice registers (or refreshes) a device for the app session it signed
// in with. A new row starts with the web's defaults: charging done, anomaly and
// software update on, plug-in reminder and milestones off. Re-registering keeps
// what is stored for every switch (only SetAPNsOptIns/SetAPNsPrefs change them), so a
// stale app posting its local defaults cannot opt anyone in.
func (s *Store) UpsertAPNsDevice(ctx context.Context, sessionID uuid.UUID, d APNsDevice) error {
	// System pool: the whole check below reads other users' registrations and sessions, which the request pool cannot see.
	tx, err := s.crossUser().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// An Apple token is one install: it belongs to whoever signed in on it last, so another account's registration of it (a
	// sign-out that never reached the server) is forgotten. Unless that registration still has a live session and a hash
	// of its install id, and the caller is not that install: then it is not ours to take (ErrDeviceElsewhere). Registrations
	// of one token are serialized by a transaction advisory lock on it, so two at once cannot both pass the check before either
	// writes (a row lock would not do: the first takes the row away, and the second would find nothing to refuse). A dead
	// session frees the token whatever the hash, and a row from an older app (no hash) moves as it always did.
	callerHash := installHash(d.InstallID)
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, d.Token); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT COALESCE(d.install_hash, ''), (s.revoked_at IS NULL AND s.expires_at > now())
		  FROM push_apns_devices d JOIN sessions s ON s.id = d.session_id
		 WHERE d.token = $1 AND d.user_id <> $2`, d.Token, s.userID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var hash string
		var live bool
		if err := rows.Scan(&hash, &live); err != nil {
			_ = rows.Close()
			return err
		}
		if live && hash != "" && hash != callerHash {
			_ = rows.Close()
			reason := "install_mismatch"
			if callerHash == "" {
				reason = "install_missing"
			}
			return ErrDeviceElsewhere{Reason: reason}
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()
	if _, err := tx.ExecContext(ctx, `DELETE FROM push_apns_devices WHERE token = $1 AND user_id <> $2`, d.Token, s.userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO push_apns_devices (id, user_id, session_id, token, env, bundle_id, install_hash)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
		ON CONFLICT (user_id, token) DO UPDATE SET
			session_id   = EXCLUDED.session_id,
			env          = EXCLUDED.env,
			bundle_id    = EXCLUDED.bundle_id,
			install_hash = COALESCE(EXCLUDED.install_hash, push_apns_devices.install_hash)`,
		uuid.New(), s.userID, sessionID, d.Token, d.Env, d.BundleID, callerHash); err != nil {
		return err
	}
	return tx.Commit()
}

// SetAPNsPrefs sets the three always-available switches on one of the user's
// devices; nil leaves a switch alone. Reports whether the token is the user's.
func (s *Store) SetAPNsPrefs(ctx context.Context, token string, chargingDone, anomaly, softwareUpdate *bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_apns_devices
		   SET on_charging_done   = COALESCE($3, on_charging_done),
		       on_anomaly         = COALESCE($4, on_anomaly),
		       on_software_update = COALESCE($5, on_software_update)
		 WHERE user_id = $1 AND token = $2`, s.userID, token, chargingDone, anomaly, softwareUpdate)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetAPNsOptIns sets the plug-in reminder and/or milestones switch (the two that
// start off) on one of the user's devices; nil leaves a switch alone.
func (s *Store) SetAPNsOptIns(ctx context.Context, token string, plugIn, milestones *bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_apns_devices
		   SET on_plug_in_reminder = COALESCE($3, on_plug_in_reminder),
		       on_milestones       = COALESCE($4, on_milestones)
		 WHERE user_id = $1 AND token = $2`, s.userID, token, plugIn, milestones)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetAPNsLeftOpen sets the left-open switch (off until the user's own control turns it on) on one of the user's
// devices. Reports whether the token is the user's.
func (s *Store) SetAPNsLeftOpen(ctx context.Context, token string, on bool) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE push_apns_devices SET on_left_open = $3 WHERE user_id = $1 AND token = $2`, s.userID, token, on)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteAPNsDevice removes one of the user's devices by token. No-op if absent.
func (s *Store) DeleteAPNsDevice(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM push_apns_devices WHERE user_id = $1 AND token = $2`, s.userID, token)
	return err
}

// deleteAPNsDeviceByID removes a device Apple reported dead.
func (s *Store) deleteAPNsDeviceByID(ctx context.Context, id uuid.UUID) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_apns_devices WHERE user_id = $1 AND id = $2`, s.userID, id)
	return err
}

func (s *Store) touchAPNsDevice(ctx context.Context, id uuid.UUID) {
	_, _ = s.db.ExecContext(ctx, `UPDATE push_apns_devices SET last_ok_at = now() WHERE user_id = $1 AND id = $2`, s.userID, id)
}

// ListLiveAPNsDevices returns the user's devices whose app session is still live:
// not revoked and not expired. A phone that signed out stops receiving pushes
// whether or not it unregistered.
func (s *Store) ListLiveAPNsDevices(ctx context.Context) ([]APNsDevice, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT d.id, d.session_id, d.token, d.env, d.bundle_id,
		       d.on_charging_done, d.on_plug_in_reminder, d.on_anomaly, d.on_software_update, d.on_milestones, d.on_left_open,
		       d.created_at, d.last_ok_at
		  FROM push_apns_devices d
		  JOIN sessions s ON s.id = d.session_id
		 WHERE d.user_id = $1 AND s.revoked_at IS NULL AND s.expires_at > now()
		 ORDER BY d.created_at`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APNsDevice
	for rows.Next() {
		var d APNsDevice
		var last sql.NullTime
		if err := rows.Scan(&d.ID, &d.SessionID, &d.Token, &d.Env, &d.BundleID,
			&d.Prefs.OnChargingDone, &d.Prefs.OnPlugInReminder, &d.Prefs.OnAnomaly, &d.Prefs.OnSoftwareUpdate, &d.Prefs.OnMilestones, &d.Prefs.OnLeftOpen,
			&d.CreatedAt, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t := last.Time
			d.LastOKAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// APNsExportRow is a device as the data export lists it: no token.
type APNsExportRow struct {
	BundleID  string      `json:"bundle_id"`
	Env       string      `json:"env"`
	CreatedAt time.Time   `json:"created_at"`
	LastOKAt  *time.Time  `json:"last_ok_at"`
	Prefs     Preferences `json:"prefs"`
}

// ExportAPNsDevices lists the user's devices for the data export, without tokens.
func (s *Store) ExportAPNsDevices(ctx context.Context) ([]APNsExportRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bundle_id, env, created_at, last_ok_at,
		       on_charging_done, on_plug_in_reminder, on_anomaly, on_software_update, on_milestones, on_left_open
		  FROM push_apns_devices WHERE user_id = $1 ORDER BY created_at`, s.userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APNsExportRow{}
	for rows.Next() {
		var r APNsExportRow
		var last sql.NullTime
		if err := rows.Scan(&r.BundleID, &r.Env, &r.CreatedAt, &last,
			&r.Prefs.OnChargingDone, &r.Prefs.OnPlugInReminder, &r.Prefs.OnAnomaly, &r.Prefs.OnSoftwareUpdate, &r.Prefs.OnMilestones, &r.Prefs.OnLeftOpen); err != nil {
			return nil, err
		}
		if last.Valid {
			t := last.Time
			r.LastOKAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Notification is the text of a push, fetched by the app after Apple delivers the
// generic one.
type Notification struct {
	ID        uuid.UUID `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// notificationKinds are the kinds that are sent to the app.
var notificationKinds = map[string]bool{
	"charging_done": true, "plug_in_reminder": true, "anomaly": true, "software_update": true, "milestone": true,
	"test": true, // the user's own "send a test" (SendAPNsTest)
}

// SafeNotificationURL keeps a notification's tap target inside the app: a
// same-site relative path, else "/" (an external link, such as a release-notes
// page, is dropped).
func SafeNotificationURL(u string) string {
	if len(u) == 0 || len(u) > 200 || u[0] != '/' || strings.HasPrefix(u, "//") || strings.ContainsRune(u, '\\') {
		return "/"
	}
	for _, r := range u {
		if unicode.IsControl(r) || r == ' ' {
			return "/"
		}
	}
	return u
}

// CreateNotification stores a notification's text for NotificationTTL and returns
// its random id. Only the known kinds are stored.
func (s *Store) CreateNotification(ctx context.Context, kind, title, body, url string) (uuid.UUID, error) {
	if !notificationKinds[kind] {
		return uuid.Nil, errors.New("push: unknown notification kind")
	}
	id := uuid.New()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO push_notifications (id, user_id, kind, title, body, url) VALUES ($1, $2, $3, $4, $5, $6)`,
		id, s.userID, kind, truncate(title, 200), truncate(body, 500), SafeNotificationURL(url))
	return id, err
}

// GetNotification returns one of the user's notifications that is under
// NotificationTTL old. Another user's id, an unknown id and an expired one are all
// ErrNotFound.
func (s *Store) GetNotification(ctx context.Context, id uuid.UUID) (Notification, error) {
	var n Notification
	err := s.db.QueryRowContext(ctx, `
		SELECT id, kind, title, body, url, created_at FROM push_notifications
		 WHERE id = $1 AND user_id = $2 AND created_at > now() - make_interval(secs => $3)`,
		id, s.userID, NotificationTTL.Seconds()).Scan(&n.ID, &n.Kind, &n.Title, &n.Body, &n.URL, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Notification{}, ErrNotFound
	}
	return n, err
}

// PurgeNotifications deletes every notification past NotificationTTL (all users).
// Run hourly.
func PurgeNotifications(ctx context.Context, db *sql.DB) (int64, error) {
	res, err := db.ExecContext(ctx,
		`DELETE FROM push_notifications WHERE created_at < now() - make_interval(secs => $1)`, NotificationTTL.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// APNsPrefs returns the five switches of one of the user's devices; ok is false
// when the token is not the user's.
func (s *Store) APNsPrefs(ctx context.Context, token string) (Preferences, bool, error) {
	var p Preferences
	err := s.db.QueryRowContext(ctx, `
		SELECT on_charging_done, on_plug_in_reminder, on_anomaly, on_software_update, on_milestones, on_left_open
		FROM push_apns_devices WHERE user_id = $1 AND token = $2`, s.userID, token).
		Scan(&p.OnChargingDone, &p.OnPlugInReminder, &p.OnAnomaly, &p.OnSoftwareUpdate, &p.OnMilestones, &p.OnLeftOpen)
	if errors.Is(err, sql.ErrNoRows) {
		return Preferences{}, false, nil
	}
	return p, err == nil, err
}
