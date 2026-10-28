package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AdminUserRow is the projection /api/admin/users serves. The
// activity counters (vehicles / drives / imports / rivian_connected
// / last_seen_at) let the admin see "did this user actually do
// anything?" without a per-user drill-down. Cheap scalar subqueries
// at our scale (<100 users); paginate if that ever changes.
type AdminUserRow struct {
	ID              uuid.UUID `json:"id"`
	Username        string    `json:"username"`
	Email           string    `json:"email,omitempty"`
	DisplayName     string    `json:"display_name,omitempty"`
	Role            string    `json:"role"`
	Disabled        bool      `json:"disabled"`
	CreatedAt       time.Time `json:"created_at"`
	VehicleCount    int       `json:"vehicle_count"`
	DriveCount      int       `json:"drive_count"`
	ImportCount     int       `json:"import_count"`
	RivianConnected bool      `json:"rivian_connected"`
	// NeedsReauth surfaces stuck Rivian sessions in the user table
	// itself — these users' drives have stopped recording and they
	// won't see the in-app banner unless they log in.
	NeedsReauth   bool       `json:"needs_reauth"`
	NeedsReauthAt *time.Time `json:"needs_reauth_at,omitempty"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	// LastDataAt is the newest vehicle_state row across the user's vehicles
	// (null when there is none): "is data still arriving".
	LastDataAt *time.Time `json:"last_data_at"`
	// Leased: one of the user's vehicles holds an unexpired subscription
	// lease, i.e. a pod is streaming it (not the finance lease).
	Leased bool `json:"leased"`
	// ReceiptsOptIn: the user opted in to reading Rivian charging receipts.
	ReceiptsOptIn bool `json:"receipts_opt_in"`
	// PurgeWarnedAt: when the inactive-account warning went out (null: not warned).
	PurgeWarnedAt *time.Time `json:"purge_warned_at"`
	// PurgeAt is when the inactive-account purge will delete the account,
	// as the job itself would schedule it: the later of signup+120 days and
	// warning+30 days, where an unsent warning is taken as going out at the
	// next run (signup+90 days, or now if that has passed). Null when the
	// account is out of the job's scope (admin, disabled, connected Rivian).
	// An account with no verified address has no warning and is due signup+120 days. It does not say whether the flag is on: see purge_enabled.
	PurgeAt *time.Time `json:"purge_at"`
}

// ListUsersForAdmin returns every user row, role-stamped and
// ordered by created_at so the admin page is stable across
// reloads. No paging — a self-hosted install will not have
// enough users to need it. If that ever stops being true,
// add LIMIT/OFFSET parameters.
func ListUsersForAdmin(ctx context.Context, d *sql.DB) ([]AdminUserRow, error) {
	if d == nil {
		return nil, nil
	}
	rows, err := d.QueryContext(ctx, `
		SELECT u.id, u.username, COALESCE(u.email, ''), COALESCE(u.display_name, ''),
		       u.role, u.disabled, u.created_at,
		       (SELECT COUNT(*) FROM vehicles WHERE user_id = u.id),
		       (SELECT COUNT(*) FROM drives   WHERE user_id = u.id),
		       (SELECT COUNT(*) FROM imports  WHERE user_id = u.id),
		       EXISTS(SELECT 1 FROM user_secrets
		               WHERE user_id = u.id AND name = 'rivian.session'),
		       u.needs_reauth, u.needs_reauth_at,
		       (SELECT MAX(last_seen_at) FROM sessions WHERE user_id = u.id),
		       -- newest state per vehicle via the (vehicle_id, at) primary key:
		       -- one index probe per vehicle, never a scan of vehicle_state
		       (SELECT MAX(vs.at) FROM vehicles v
		          CROSS JOIN LATERAL (SELECT at FROM vehicle_state
		                               WHERE vehicle_id = v.id ORDER BY at DESC LIMIT 1) vs
		         WHERE v.user_id = u.id),
		       EXISTS(SELECT 1 FROM vehicles v
		                JOIN subscription_leases l ON l.vehicle_id = v.rivian_vehicle_id
		               WHERE v.user_id = u.id AND l.expires_at > now()),
		       EXISTS(SELECT 1 FROM user_settings s
		               WHERE s.user_id = u.id AND s.value->>'privacy.rivian_receipts' = '1'),
		       u.inactive_warned_at,
		       CASE WHEN `+inactiveScope+` AND COALESCE(u.email, '') <> ''
		            THEN GREATEST(u.created_at + $1::interval,
		                          COALESCE(u.inactive_warned_at, GREATEST(u.created_at + $2::interval, now())) + $3::interval)
		            -- no verified address to warn: deleted at day 120, no warning
		            WHEN `+unwarnableScope+` THEN `+unwarnableClock("$4")+` + $1::interval
		       END
		FROM users u
		ORDER BY u.created_at ASC
	`, pgInterval(InactiveDeleteAfter), pgInterval(InactiveWarnAfter), pgInterval(InactiveWarnGrace), UnwarnablePolicyStart)
	if err != nil {
		return nil, fmt.Errorf("list users for admin: %w", err)
	}
	defer rows.Close()
	var out []AdminUserRow
	for rows.Next() {
		var r AdminUserRow
		var lastSeen, reauthAt, lastData, warned, purgeAt sql.NullTime
		if err := rows.Scan(
			&r.ID, &r.Username, &r.Email, &r.DisplayName, &r.Role, &r.Disabled, &r.CreatedAt,
			&r.VehicleCount, &r.DriveCount, &r.ImportCount, &r.RivianConnected,
			&r.NeedsReauth, &reauthAt, &lastSeen, &lastData, &r.Leased, &r.ReceiptsOptIn, &warned, &purgeAt,
		); err != nil {
			return nil, fmt.Errorf("list users for admin scan: %w", err)
		}
		if reauthAt.Valid {
			t := reauthAt.Time
			r.NeedsReauthAt = &t
		}
		if lastSeen.Valid {
			t := lastSeen.Time
			r.LastSeenAt = &t
		}
		if lastData.Valid {
			t := lastData.Time
			r.LastDataAt = &t
		}
		if warned.Valid {
			t := warned.Time
			r.PurgeWarnedAt = &t
		}
		if purgeAt.Valid {
			t := purgeAt.Time
			r.PurgeAt = &t
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AdminUserDetail is the deep-dive bundle the admin drawer renders
// when the operator drills into a single user. Everything that helps
// answer "is this user healthy / why aren't they seeing data?" in
// one round-trip: identity, Rivian session age, per-vehicle telemetry
// freshness, lifetime totals, signup source, active session count.
type AdminUserDetail struct {
	ID             uuid.UUID `json:"id"`
	Username       string    `json:"username"`
	Email          string    `json:"email,omitempty"`
	DisplayName    string    `json:"display_name,omitempty"`
	Role           string    `json:"role"`
	Disabled       bool      `json:"disabled"`
	OnboardingDone bool      `json:"onboarding_completed"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`

	// Rivian state (DB-only — runtime in-memory state is per-pod and
	// not authoritative; admins should trust what's persisted).
	RivianConnected   bool       `json:"rivian_connected"`
	RivianSessionAt   *time.Time `json:"rivian_session_at,omitempty"`
	NeedsReauth       bool       `json:"needs_reauth"`
	NeedsReauthReason string     `json:"needs_reauth_reason,omitempty"`
	NeedsReauthAt     *time.Time `json:"needs_reauth_at,omitempty"`

	// Activity rollups.
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	ActiveSessions  int        `json:"active_sessions"`
	DriveCount      int        `json:"drive_count"`
	DriveMilesTotal float64    `json:"drive_miles_total"`
	ChargeCount     int        `json:"charge_count"`
	ChargeKWhTotal  float64    `json:"charge_kwh_total"`
	SampleCount     int64      `json:"sample_count"`
	OldestSampleAt  *time.Time `json:"oldest_sample_at,omitempty"`
	NewestSampleAt  *time.Time `json:"newest_sample_at,omitempty"`
	ImportCount     int        `json:"import_count"`

	Vehicles      []AdminUserVehicle   `json:"vehicles"`
	SignupRequest *AdminSignupSnapshot `json:"signup_request,omitempty"`
}

// AdminUserVehicle is one vehicle row enriched with the bits the
// admin drawer wants — telemetry recency and per-vehicle counts.
type AdminUserVehicle struct {
	ID              uuid.UUID  `json:"id"`
	RivianVehicleID string     `json:"rivian_vehicle_id"`
	VIN             string     `json:"vin,omitempty"`
	DisplayName     string     `json:"display_name,omitempty"`
	Model           string     `json:"model,omitempty"`
	ModelYear       int        `json:"model_year,omitempty"`
	PackKWh         float64    `json:"pack_kwh,omitempty"`
	DriveCount      int        `json:"drive_count"`
	ChargeCount     int        `json:"charge_count"`
	LastSampleAt    *time.Time `json:"last_sample_at,omitempty"`
}

// AdminSignupSnapshot captures how this user came to exist when the
// signup_requests waitlist was the entry path. Nil for users created
// directly via the admin "create user" flow or seed scripts.
type AdminSignupSnapshot struct {
	RequestedAt time.Time  `json:"requested_at"`
	DecidedAt   *time.Time `json:"decided_at,omitempty"`
	Status      string     `json:"status"`
	Message     string     `json:"message,omitempty"`
}

// GetUserDetailForAdmin assembles the per-user bundle as several
// short queries against the same connection. Multi-statement single
// query would be marginally faster but harder to reason about; with
// <100 users and an indexed (user_id) on every dependent table the
// fan-out cost is invisible.
func GetUserDetailForAdmin(ctx context.Context, d *sql.DB, uid uuid.UUID) (*AdminUserDetail, error) {
	if d == nil || uid == uuid.Nil {
		return nil, nil
	}
	// Initialise Vehicles to an empty slice so the JSON encoder
	// emits `[]` instead of `null` for users with zero vehicles —
	// the SPA reads `.vehicles.length` directly.
	out := &AdminUserDetail{ID: uid, Vehicles: []AdminUserVehicle{}}

	// Basic identity + Rivian flags + session-row presence/age in one
	// shot. LEFT JOIN on user_secrets keeps a NULL-bearing row when
	// the user hasn't connected Rivian yet.
	var rivianAt sql.NullTime
	var needsReauthAt sql.NullTime
	err := d.QueryRowContext(ctx, `
		SELECT u.username, COALESCE(u.email, ''), COALESCE(u.display_name, ''),
		       u.role, u.disabled, COALESCE(u.onboarding_completed, false),
		       u.created_at, u.updated_at,
		       u.needs_reauth, COALESCE(u.needs_reauth_reason, ''), u.needs_reauth_at,
		       s.updated_at IS NOT NULL,
		       s.updated_at
		FROM users u
		LEFT JOIN user_secrets s
		       ON s.user_id = u.id AND s.name = 'rivian.session'
		WHERE u.id = $1
	`, uid).Scan(
		&out.Username, &out.Email, &out.DisplayName,
		&out.Role, &out.Disabled, &out.OnboardingDone,
		&out.CreatedAt, &out.UpdatedAt,
		&out.NeedsReauth, &out.NeedsReauthReason, &needsReauthAt,
		&out.RivianConnected, &rivianAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("admin user detail: %w", err)
	}
	if rivianAt.Valid {
		t := rivianAt.Time
		out.RivianSessionAt = &t
	}
	if needsReauthAt.Valid {
		t := needsReauthAt.Time
		out.NeedsReauthAt = &t
	}

	// Activity rollups in one round-trip. COALESCE wraps the
	// aggregate SUMs so an empty drives/charges table returns 0
	// instead of NULL.
	var lastSeen, oldestSample, newestSample sql.NullTime
	if err := d.QueryRowContext(ctx, `
		SELECT
		  (SELECT MAX(last_seen_at) FROM sessions
		     WHERE user_id = $1),
		  (SELECT COUNT(*) FROM sessions
		     WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > NOW()),
		  (SELECT COUNT(*) FROM drives WHERE user_id = $1),
		  COALESCE((SELECT SUM(distance_mi) FROM drives WHERE user_id = $1), 0),
		  (SELECT COUNT(*) FROM charges WHERE user_id = $1),
		  COALESCE((SELECT SUM(energy_added_kwh) FROM charges WHERE user_id = $1), 0),
		  (SELECT COUNT(*) FROM vehicle_state WHERE user_id = $1),
		  (SELECT MIN(at) FROM vehicle_state WHERE user_id = $1),
		  (SELECT MAX(at) FROM vehicle_state WHERE user_id = $1),
		  (SELECT COUNT(*) FROM imports WHERE user_id = $1)
	`, uid).Scan(
		&lastSeen, &out.ActiveSessions,
		&out.DriveCount, &out.DriveMilesTotal,
		&out.ChargeCount, &out.ChargeKWhTotal,
		&out.SampleCount, &oldestSample, &newestSample,
		&out.ImportCount,
	); err != nil {
		return nil, fmt.Errorf("admin user detail rollups: %w", err)
	}
	if lastSeen.Valid {
		t := lastSeen.Time
		out.LastSeenAt = &t
	}
	if oldestSample.Valid {
		t := oldestSample.Time
		out.OldestSampleAt = &t
	}
	if newestSample.Valid {
		t := newestSample.Time
		out.NewestSampleAt = &t
	}

	// Per-vehicle bundle. vehicle_state.vehicle_id is the internal
	// UUID (vehicles.id), not the Rivian gateway string; the drives
	// + charges tables follow the same convention.
	rows, err := d.QueryContext(ctx, `
		SELECT v.id, v.rivian_vehicle_id,
		       COALESCE(v.vin, ''), COALESCE(v.display_name, ''),
		       COALESCE(v.model, ''), COALESCE(v.model_year, 0),
		       COALESCE(v.pack_kwh, 0),
		       (SELECT COUNT(*) FROM drives WHERE vehicle_id = v.id),
		       (SELECT COUNT(*) FROM charges WHERE vehicle_id = v.id),
		       (SELECT MAX(at) FROM vehicle_state WHERE vehicle_id = v.id)
		FROM vehicles v
		WHERE v.user_id = $1
		ORDER BY v.created_at ASC
	`, uid)
	if err != nil {
		return nil, fmt.Errorf("admin user detail vehicles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v AdminUserVehicle
		var lastSample sql.NullTime
		if err := rows.Scan(
			&v.ID, &v.RivianVehicleID,
			&v.VIN, &v.DisplayName,
			&v.Model, &v.ModelYear, &v.PackKWh,
			&v.DriveCount, &v.ChargeCount, &lastSample,
		); err != nil {
			return nil, fmt.Errorf("admin user detail vehicles scan: %w", err)
		}
		if lastSample.Valid {
			t := lastSample.Time
			v.LastSampleAt = &t
		}
		out.Vehicles = append(out.Vehicles, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("admin user detail vehicles iter: %w", err)
	}

	// Signup source. The signup_requests table joins by email rather
	// than user_id, so we look up the most recent request matching
	// the user's email. Absent for direct admin-created users.
	if out.Email != "" {
		var sr AdminSignupSnapshot
		var decidedAt sql.NullTime
		var msg sql.NullString
		switch err := d.QueryRowContext(ctx, `
			SELECT requested_at, decided_at, status, COALESCE(message, '')
			FROM signup_requests
			WHERE LOWER(email) = LOWER($1)
			ORDER BY requested_at DESC
			LIMIT 1
		`, out.Email).Scan(&sr.RequestedAt, &decidedAt, &sr.Status, &msg); {
		case err == sql.ErrNoRows:
			// No signup request — direct admin-created user, fine.
		case err != nil:
			return nil, fmt.Errorf("admin user detail signup: %w", err)
		default:
			if decidedAt.Valid {
				t := decidedAt.Time
				sr.DecidedAt = &t
			}
			if msg.Valid {
				sr.Message = msg.String
			}
			out.SignupRequest = &sr
		}
	}

	return out, nil
}

// DeleteUser removes a user row. ON DELETE CASCADE on every
// dependent table (vehicles, drives, charges, samples,
// user_settings, user_secrets, sessions, push_subscriptions, …)
// means this is sufficient to fully evict the tenant; the admin
// endpoint relies on that contract instead of a hand-rolled
// transactional sweep.
//
// subscription_leases is the one exception: it's keyed by
// rivian_vehicle_id with no FK back to users, so the cascade misses
// it. A surviving lease is renewed forever by its owning pod, whose
// recorder then fails FK inserts against the now-deleted vehicle
// indefinitely. Drop the leases in the same transaction, while the
// vehicles rows still exist to resolve their ids, so the owning pod
// sees them vanish on its next renew and tears the subscription down.
//
// charges_deleted_backup (repair-phantom-charges' undo copies) has no FK
// either: it must outlive the charges it backs up. A deleted user's
// copies go with them.
func DeleteUser(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	if d == nil || uid == uuid.Nil {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := deleteUserTx(ctx, tx, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// deleteUserTx is DeleteUser's body inside a caller's transaction.
func deleteUserTx(ctx context.Context, tx *sql.Tx, uid uuid.UUID) error {
	if err := lockArchiveUserTx(ctx, tx, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM subscription_leases
		 WHERE vehicle_id IN (
		     SELECT rivian_vehicle_id FROM vehicles WHERE user_id = $1
		 )`, uid); err != nil {
		return err
	}
	// The user's own group lease row (M1 of the single-owner design; a group's id
	// is its smallest user id). The holder also drops it on its next pass.
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_leases WHERE group_id = $1`, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM charges_deleted_backup WHERE user_id = $1`, uid); err != nil {
		return err
	}
	// The cascade below removes the user's archive key (crypto-shredding every
	// archive object) and archive_months; queue the objects themselves for the
	// deletion job, in the same transaction.
	if err := queueArchiveDeletionTx(ctx, tx, uid); err != nil {
		return err
	}
	if err := deleteFromThinnedCopiesTx(ctx, tx, uid); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, uid)
	return err
}

// deleteFromThinnedCopiesTx removes uid's rows from the full copies sample
// thinning keeps detached for a week (vehicle_state_yYYYY_MM_full): they are
// outside vehicle_state, so the users cascade doesn't reach them.
func deleteFromThinnedCopiesTx(ctx context.Context, tx *sql.Tx, uid uuid.UUID) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = current_schema() AND c.relkind = 'r'
		   AND c.relname ~ '^vehicle_state_y[0-9]{4}_[0-9]{2}_full$'`)
	if err != nil {
		return err
	}
	var tables []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, t := range tables {
		// The name comes from the catalog and matches the pattern above.
		if _, err := tx.ExecContext(ctx, `DELETE FROM "`+t+`" WHERE user_id = $1`, uid); err != nil {
			return err
		}
	}
	return nil
}

// lockArchiveUserTx takes uid's archive lock for the transaction: an archive
// object being written for uid finishes first (internal/archive holds the
// same lock, archive.userLockSQL), so the deletion queued after it covers that
// object too. Taken before any row lock, so the archive job (which takes row
// locks while holding it) can't deadlock with us.
func lockArchiveUserTx(ctx context.Context, tx *sql.Tx, uid uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('rivolt-archive-user:' || $1::uuid::text, 0))`, uid)
	return err
}

// queueArchiveDeletionTx queues every telemetry archive object of uid for
// deletion (internal/archive.Deleter). Only a user with an archive key can
// have objects, so nothing is queued otherwise. The prefix is relative to the
// archive's environment ("v1/u=<uid>/"). requested_at is clock_timestamp(),
// not NOW() (the transaction's start, before it waited for the archive
// lock): the deletion covers objects written up to the moment it is queued.
func queueArchiveDeletionTx(ctx context.Context, tx *sql.Tx, uid uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO archive_deletions (user_id, prefix, requested_at)
		SELECT $1::uuid, 'v1/u=' || $1::uuid::text || '/', clock_timestamp()
		 WHERE EXISTS (SELECT 1 FROM archive_keys WHERE user_id = $1::uuid)`, uid)
	return err
}

// ResetUserArchive is "delete your history" for the telemetry archive: it
// queues uid's objects for deletion and removes their archive_months rows and
// the user's data key, so the objects are unreadable at once and gone after
// the deletion job's next run. A later archive run creates a new key. It also
// clears uid's rows from thinning's detached full copies.
func ResetUserArchive(ctx context.Context, d *sql.DB, uid uuid.UUID) error {
	if d == nil || uid == uuid.Nil {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockArchiveUserTx(ctx, tx, uid); err != nil {
		return err
	}
	if err := queueArchiveDeletionTx(ctx, tx, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_months WHERE user_id = $1`, uid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_keys WHERE user_id = $1`, uid); err != nil {
		return err
	}
	if err := deleteFromThinnedCopiesTx(ctx, tx, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// ListUsersWithRivianSession returns every user_id that currently has
// a persisted rivian.session blob in user_secrets. The boot path
// uses this to hydrate every user's *LiveClient before the
// StateMonitor goroutines start, so a pod restart is invisible to
// the data plane regardless of which users are active.
//
// The query reads ciphertext-presence only — it does NOT decrypt
// the blob, so it's safe to run before the secret-sealer is fully
// wired (e.g. during a future rotation window).
func ListUsersWithRivianSession(ctx context.Context, db *sql.DB) ([]uuid.UUID, error) {
	if db == nil {
		return nil, nil
	}
	const q = `SELECT user_id FROM user_secrets WHERE name = 'rivian.session'`
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list users with rivian session: %w", err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("list users with rivian session scan: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// VehicleOwner resolves an internal vehicles.id (UUID) back to its
// owning user_id. Used by the StateMonitor recorder to attribute
// frames to the right user even when a single pod monitors vehicles
// belonging to many users (post-PR-6 lease coordinator).
//
// Returns (uuid.Nil, nil) when no row exists; callers should treat
// that as "stale subscription, drop the frame" rather than an error.
func VehicleOwner(ctx context.Context, db *sql.DB, vehicleID uuid.UUID) (uuid.UUID, error) {
	if db == nil || vehicleID == uuid.Nil {
		return uuid.Nil, nil
	}
	var owner uuid.UUID
	err := db.QueryRowContext(ctx,
		`SELECT user_id FROM vehicles WHERE id = $1`, vehicleID).Scan(&owner)
	if err == sql.ErrNoRows {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("vehicle owner: %w", err)
	}
	return owner, nil
}

// VehicleOwnerByRivianID is the Rivian-gateway-id-keyed sibling of
// VehicleOwner. The lease coordinator queries this when it acquires
// a lease so the per-user *LiveClient can be hydrated before the WS
// subscription starts.
func VehicleOwnerByRivianID(ctx context.Context, db *sql.DB, rivianID string) (uuid.UUID, error) {
	if db == nil || rivianID == "" {
		return uuid.Nil, nil
	}
	var owner uuid.UUID
	err := db.QueryRowContext(ctx,
		`SELECT user_id FROM vehicles WHERE rivian_vehicle_id = $1 LIMIT 1`,
		rivianID).Scan(&owner)
	if err == sql.ErrNoRows {
		return uuid.Nil, nil
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("vehicle owner by rivian id: %w", err)
	}
	return owner, nil
}

// ListVehiclesWithOwners returns every (user_id, rivian_vehicle_id)
// tuple in the vehicles table. Used by the lease coordinator's
// vehicle source so the cluster's reconciliation set is keyed by
// user, not by rivian id alone — two users with the same rivian id
// (yes, this is possible: shared family account, dev/prod accidental
// reuse) are distinct lease subjects.
type VehicleOwnership struct {
	UserID    uuid.UUID
	VehicleID string // rivian gateway id
}

// ListVehiclesWithOwners returns every (user_id, rivian_vehicle_id)
// pair in the vehicles table. Empty rivian ids are skipped — the row
// is a placeholder waiting for first contact, not a subscription
// candidate.
func ListVehiclesWithOwners(ctx context.Context, db *sql.DB) ([]VehicleOwnership, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.QueryContext(ctx, `
		SELECT user_id, rivian_vehicle_id
		FROM vehicles
		WHERE rivian_vehicle_id <> ''`)
	if err != nil {
		return nil, fmt.Errorf("list vehicles with owners: %w", err)
	}
	defer rows.Close()
	var out []VehicleOwnership
	for rows.Next() {
		var v VehicleOwnership
		if err := rows.Scan(&v.UserID, &v.VehicleID); err != nil {
			return nil, fmt.Errorf("list vehicles with owners scan: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
