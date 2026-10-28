package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// MaxSetupNudges caps the setup reminder emails per user: a first one and one
// follow-up. With the welcome email that is three emails ever. More than that
// stops being a reminder.
const MaxSetupNudges = 2

// WelcomeDelay is how long after signup the welcome email goes out, so a user
// still in the middle of connecting is not emailed in the same minute.
const WelcomeDelay = 10 * time.Minute

// Setup-email kinds: the nudge copy differs by what is missing.
const (
	NudgeNoVehicle = "no_vehicle" // never connected Rivian
	NudgeNoData    = "no_data"    // connected, but no state, drive or charge arrived
)

// SetupNudge is a user due a setup reminder. Attempt is which reminder this
// will be (1 or 2) so the sender can vary the copy.
type SetupNudge struct {
	UserID  uuid.UUID
	Email   string
	Attempt int
	Kind    string
}

// Welcome is a user due the welcome email.
type Welcome struct {
	UserID uuid.UUID
	Email  string
	// HasApp: the account has a live iPhone-app session, so the welcome leaves the app link out.
	HasApp bool
}

// emailable is the common eligibility for every welcome/setup email: a
// deliverable VERIFIED address (the statement email's rule; not one Apple told us
// its relay no longer forwards to), not disabled, not opted out. The demo account has no email so it never matches.
const emailable = `u.disabled = FALSE
		  AND u.email_verified
		  AND NOT u.email_undeliverable
		  AND COALESCE(u.email, '') <> ''
		  AND u.engagement_emails_off_at IS NULL`

// noData is true while the account has nothing to show: no state sample (via
// its vehicles), no drive and no charge. A user with no vehicle row at all
// satisfies it too.
const noData = `NOT EXISTS (SELECT 1 FROM vehicles v JOIN vehicle_state vs ON vs.vehicle_id = v.id WHERE v.user_id = u.id)
		  AND NOT EXISTS (SELECT 1 FROM drives dr WHERE dr.user_id = u.id)
		  AND NOT EXISTS (SELECT 1 FROM charges ch WHERE ch.user_id = u.id)`

// dueBatch bounds one sweep's candidate list per kind.
const dueBatch = 100

// ListUsersDueForWelcome returns users due the welcome email: at least
// WelcomeDelay old, never welcomed, emailable, and not yet connected (a user
// who connected within the delay skips it). Candidate list only; the caller
// must ClaimWelcome each one.
func ListUsersDueForWelcome(ctx context.Context, d *sql.DB) ([]Welcome, error) {
	q := `SELECT u.id, u.email,
		       EXISTS (SELECT 1 FROM sessions s WHERE s.user_id = u.id AND s.kind = 'app' AND s.revoked_at IS NULL AND s.expires_at > now())
		FROM users u
		WHERE u.welcome_sent_at IS NULL
		  AND ` + emailable + `
		  AND u.created_at <= now() - $1::interval
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)
		ORDER BY u.created_at
		LIMIT ` + fmt.Sprint(dueBatch)
	rows, err := d.QueryContext(ctx, q, pgInterval(WelcomeDelay))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Welcome
	for rows.Next() {
		var w Welcome
		if err := rows.Scan(&w.UserID, &w.Email, &w.HasApp); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ClaimWelcome records that the welcome email is being sent, true only if
// this caller won (once per user, safe on every replica). It re-checks
// eligibility so an opt-out or a connect since the list was built wins.
func ClaimWelcome(ctx context.Context, d *sql.DB, userID uuid.UUID) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE users u SET welcome_sent_at = now()
		WHERE u.id = $1 AND u.welcome_sent_at IS NULL
		  AND `+emailable+`
		  AND NOT EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id)`, userID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ListUsersDueForSetupNudge returns users who still have no data (no vehicle,
// or a vehicle that has produced no state, drive or charge), are emailable,
// and are due their next setup reminder:
//
//   - the first once the account is at least minAge old, and
//   - the follow-up once followUpAfter has passed since the first.
//
// Onboarding state is deliberately not a filter: many users who stall never
// finish onboarding either. Candidate list only; the caller must
// ClaimSetupNudge each one so the sweep is safe on every replica.
func ListUsersDueForSetupNudge(ctx context.Context, d *sql.DB, minAge, followUpAfter time.Duration) ([]SetupNudge, error) {
	q := `SELECT u.id, u.email, u.setup_nudge_count + 1,
		       CASE WHEN EXISTS (SELECT 1 FROM vehicles v WHERE v.user_id = u.id) THEN '` + NudgeNoData + `' ELSE '` + NudgeNoVehicle + `' END
		FROM users u
		WHERE u.setup_nudge_count < $3
		  AND ` + emailable + `
		  AND u.created_at <= now() - $1::interval
		  AND (u.setup_nudged_at IS NULL OR u.setup_nudged_at <= now() - $2::interval)
		  AND ` + noData + `
		ORDER BY u.created_at
		LIMIT ` + fmt.Sprint(dueBatch)
	rows, err := d.QueryContext(ctx, q, pgInterval(minAge), pgInterval(followUpAfter), MaxSetupNudges)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SetupNudge
	for rows.Next() {
		var n SetupNudge
		if err := rows.Scan(&n.UserID, &n.Email, &n.Attempt, &n.Kind); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ClaimSetupNudge atomically records that setup reminder number attempt is
// being sent, returning true only if the claim was won. The WHERE re-checks
// eligibility and pins the count to attempt-1, so data that arrived since the
// candidate list was built, an opt-out, or a peer replica that got there
// first makes the claim a no-op. Each attempt can be claimed once.
func ClaimSetupNudge(ctx context.Context, d *sql.DB, userID uuid.UUID, attempt int) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE users u
		SET setup_nudged_at = now(), setup_nudge_count = $2
		WHERE u.id = $1
		  AND u.setup_nudge_count = $2 - 1
		  AND `+emailable+`
		  AND `+noData, userID, attempt)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// OnboardingEmails is what we hold about the welcome and setup emails for one
// user (the data export includes it).
type OnboardingEmails struct {
	WelcomeSentAt *time.Time
	NudgesSent    int
	OffAt         *time.Time
}

// OnboardingEmailsFor reads it; the zero value for an unknown user.
func OnboardingEmailsFor(ctx context.Context, d *sql.DB, userID uuid.UUID) (OnboardingEmails, error) {
	var o OnboardingEmails
	var w, off sql.NullTime
	err := d.QueryRowContext(ctx, `SELECT welcome_sent_at, setup_nudge_count, engagement_emails_off_at FROM users WHERE id = $1`, userID).
		Scan(&w, &o.NudgesSent, &off)
	if err == sql.ErrNoRows {
		return o, nil
	}
	if err != nil {
		return o, err
	}
	if w.Valid {
		o.WelcomeSentAt = &w.Time
	}
	if off.Valid {
		o.OffAt = &off.Time
	}
	return o, nil
}

// EngagementEmailsOff reports whether the user turned the welcome and setup
// emails off.
func EngagementEmailsOff(ctx context.Context, d *sql.DB, userID uuid.UUID) (bool, error) {
	var off sql.NullTime
	err := d.QueryRowContext(ctx, `SELECT engagement_emails_off_at FROM users WHERE id = $1`, userID).Scan(&off)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return off.Valid, err
}

// SetEngagementEmailsOff turns the welcome and setup emails off or on.
// Idempotent: turning off again keeps the original time.
func SetEngagementEmailsOff(ctx context.Context, d *sql.DB, userID uuid.UUID, off bool) error {
	q := `UPDATE users SET engagement_emails_off_at = NULL WHERE id = $1`
	if off {
		q = `UPDATE users SET engagement_emails_off_at = COALESCE(engagement_emails_off_at, now()) WHERE id = $1`
	}
	_, err := d.ExecContext(ctx, q, userID)
	return err
}

func pgInterval(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}
