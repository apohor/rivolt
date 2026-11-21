package entitlements

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// Operator grants: a free Pro period for one account, written by `rivolt grant-pro` and by nothing else (a CLI rather than an admin-site
// button: no new HTTP surface, and every run is one reviewed command). The row is source grant in the entitlements table, which the plan
// logic already ranks (below founding, above trial) and ends on its date; the reason lives in the admin audit log, not in the row, because
// the table has no column for it and the audit log is where every operator write is read back.

const (
	// GrantMaxDays is how far ahead a grant may end.
	GrantMaxDays = 400
	// grantReasonMax bounds the reason stored in the audit row.
	grantReasonMax = 200
	// Audit actions of the two forms.
	AuditGrant  = "entitlement.grant"
	AuditRevoke = "entitlement.revoke_grant"
	// grantAuditSource labels the audit row's access_email: the CLI runs from the operator's shell, so there is no signed-in actor (the actor is the nil id).
	grantAuditSource = "cli:grant-pro"
)

// Refusals of a grant, so a test can tell them apart.
var (
	ErrGrantNoAccount   = errors.New("no account has that sign-in email")
	ErrGrantManyAccount = errors.New("more than one account has that sign-in email")
	ErrGrantDemo        = errors.New("that is the demo account")
	ErrGrantDisabled    = errors.New("that account is disabled")
	ErrGrantCovered     = errors.New("the account already has Pro that ends later")
	ErrGrantNoRow       = errors.New("the account has no running grant to revoke")
)

// GrantEnd turns the --until date (YYYY-MM-DD) into the instant the grant ends: the last second of that UTC day, so Pro runs through the
// date the client shows ("free until <date>" is the UTC calendar date of this instant). It refuses a date that is not after today and one
// more than GrantMaxDays ahead.
func GrantEnd(date string, now time.Time) (time.Time, error) {
	d, err := time.Parse("2006-01-02", strings.TrimSpace(date))
	if err != nil {
		return time.Time{}, errors.New("--until must be a date, YYYY-MM-DD")
	}
	today := now.UTC().Truncate(24 * time.Hour)
	if !d.After(today) {
		return time.Time{}, fmt.Errorf("--until %s is not after today (%s)", d.Format("2006-01-02"), today.Format("2006-01-02"))
	}
	if d.After(today.AddDate(0, 0, GrantMaxDays)) {
		return time.Time{}, fmt.Errorf("--until %s is more than %d days ahead (latest %s)", d.Format("2006-01-02"), GrantMaxDays, today.AddDate(0, 0, GrantMaxDays).Format("2006-01-02"))
	}
	return d.Add(24*time.Hour - time.Second), nil
}

// CheckGrantReason refuses an empty, over-long or control-character reason, and one that holds an address: the reason is stored in the audit log
// and says why, not who.
func CheckGrantReason(reason string) (string, error) {
	r := strings.TrimSpace(reason)
	switch {
	case r == "":
		return "", errors.New("--reason is required")
	case len([]rune(r)) > grantReasonMax:
		return "", fmt.Errorf("--reason is longer than %d characters", grantReasonMax)
	case strings.Contains(r, "@"):
		return "", errors.New("--reason must not contain an email address (say why, not who)")
	}
	for _, c := range r {
		if unicode.IsControl(c) {
			return "", errors.New("--reason must be one line of plain text")
		}
	}
	return r, nil
}

// MaskEmail shows enough of an address to recognise it and not enough to read it back: "a***@e***.com".
func MaskEmail(addr string) string {
	local, domain, ok := strings.Cut(addr, "@")
	if !ok || local == "" || domain == "" {
		return "***"
	}
	first := func(s string) string { return string([]rune(s)[:1]) }
	host, tld := domain, ""
	if i := strings.LastIndex(domain, "."); i > 0 {
		host, tld = domain[:i], domain[i:]
	}
	return first(local) + "***@" + first(host) + "***" + tld
}

// CheckGrant is why a grant ending at end must not be written for an account with these rows, or nil. A paid plan that is running (Stripe or
// Apple, in its grace too) is never shadowed, whatever its end; a running founding year is refused too (its end-of-year notice would be wrong beside a grant); trial and an earlier grant are only refused when they end at or
// after the new end, so a grant never shortens what the account already has. The error says what it found.
func CheckGrant(rows []Row, end, now time.Time) error {
	for _, r := range rows {
		pe, grants := grantEnd(r, now)
		if !grants {
			continue
		}
		switch r.Source {
		case SourceStripe, SourceApple:
			return fmt.Errorf("%w: it has a paid %s plan (%s%s)", ErrGrantCovered, r.Source, r.Status, endText(pe))
		case SourceFounding:
			// The founding-end notice and the plan card promise the account's Pro ends with the founding year; a grant beside a running
			// founding row would make both untrue. Grant after it ends.
			return fmt.Errorf("%w: it has founding Pro (%s%s); grant after it ends", ErrGrantCovered, r.Status, endText(pe))
		default:
			if pe == nil || !pe.Before(end) {
				return fmt.Errorf("%w: it has %s Pro (%s%s)", ErrGrantCovered, r.Source, r.Status, endText(pe))
			}
		}
	}
	return nil
}

func endText(end *time.Time) string {
	if end == nil {
		return ", no end"
	}
	return ", until " + end.UTC().Format("2006-01-02")
}

// GrantParams is one run of `rivolt grant-pro`.
type GrantParams struct {
	Email  string
	Until  string // YYYY-MM-DD; unused when Revoke
	Reason string
	Revoke bool
	DryRun bool
}

// validateGrant checks everything about a run that needs no database: the address, the reason, the date. end is the grant's end (zero for a revoke).
func validateGrant(p GrantParams, now time.Time) (end time.Time, err error) {
	addr := strings.TrimSpace(p.Email)
	if strings.Count(addr, "@") != 1 || len(addr) > 254 || strings.ContainsAny(addr, " \t\r\n") {
		return end, errors.New("--email must be one sign-in email address")
	}
	if _, err = CheckGrantReason(p.Reason); err != nil {
		return end, err
	}
	if p.Revoke {
		if p.Until != "" {
			return end, errors.New("--revoke takes no --until")
		}
		return end, nil
	}
	return GrantEnd(p.Until, now)
}

// CheckGrantFlags is validateGrant for the command: it prints the refusal and returns the usage exit code, or 0.
func CheckGrantFlags(p GrantParams, now time.Time) int {
	if _, err := validateGrant(p, now); err != nil {
		fmt.Fprintln(os.Stderr, "grant-pro:", err)
		return 2
	}
	return 0
}

// RunGrantPro is the body of `rivolt grant-pro` (here, so a test drives it against Postgres without the web bundle). It returns the exit code:
// 0 done (or a dry run that would be), 1 refused or failed, 2 usage. One run touches one account, in one transaction that holds the entitlement
// write and its audit row, so neither exists without the other. Output names the account only as a masked address.
func RunGrantPro(ctx context.Context, pool *sql.DB, out, errOut io.Writer, demo uuid.UUID, now time.Time, p GrantParams) int {
	fail := func(code int, err error) int { fmt.Fprintln(errOut, "grant-pro:", err); return code }
	end, err := validateGrant(p, now)
	if err != nil {
		return fail(2, err)
	}
	reason := strings.TrimSpace(p.Reason)
	addr := strings.TrimSpace(p.Email)

	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		return fail(1, err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Exactly one account by sign-in email, case-insensitive. Two rows is enough to refuse.
	rs, err := tx.QueryContext(ctx, `SELECT id, disabled, email_verified FROM users WHERE lower(email) = lower($1) LIMIT 2 FOR UPDATE`, addr)
	if err != nil {
		return fail(1, err)
	}
	var ids []uuid.UUID
	var disabled, verified bool
	for rs.Next() {
		var id uuid.UUID
		if err := rs.Scan(&id, &disabled, &verified); err != nil {
			rs.Close()
			return fail(1, err)
		}
		ids = append(ids, id)
	}
	rs.Close()
	if err := rs.Err(); err != nil {
		return fail(1, err)
	}
	masked := MaskEmail(addr)
	switch {
	case len(ids) == 0:
		return fail(1, fmt.Errorf("%w (%s). Nothing was written", ErrGrantNoAccount, masked))
	case len(ids) > 1:
		return fail(1, fmt.Errorf("%w (%s). Nothing was written", ErrGrantManyAccount, masked))
	case ids[0] == demo:
		return fail(1, fmt.Errorf("%w. Nothing was written", ErrGrantDemo))
	case disabled:
		return fail(1, fmt.Errorf("%w (%s). Nothing was written", ErrGrantDisabled, masked))
	}
	uid := ids[0]
	fmt.Fprintf(out, "account %s, email verified: %v\n", masked, verified)

	rows, err := grantRows(ctx, tx, uid)
	if err != nil {
		return fail(1, err)
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "found: no entitlement rows (a free account)")
	}
	for _, r := range rows {
		fmt.Fprintf(out, "found: %s, %s%s\n", r.Source, r.Status, endText(r.PeriodEnd))
	}

	action, details := AuditGrant, map[string]any{"source": SourceGrant, "reason": reason}
	if p.Revoke {
		action = AuditRevoke
		var live *Row
		for i := range rows {
			if rows[i].Source == SourceGrant {
				if _, grants := grantEnd(rows[i], now); grants {
					live = &rows[i]
				}
			}
		}
		if live == nil {
			return fail(1, fmt.Errorf("%w (%s). Nothing was written", ErrGrantNoRow, masked))
		}
		fmt.Fprintf(out, "plan: end the grant that runs%s\n", endText(live.PeriodEnd))
	} else {
		if err := CheckGrant(rows, end, now); err != nil {
			return fail(1, fmt.Errorf("%w. Nothing was written", err))
		}
		details["until"] = end.Format("2006-01-02")
		fmt.Fprintf(out, "plan: grant Pro until %s (source grant)\n", end.Format("2006-01-02"))
	}
	if p.DryRun {
		fmt.Fprintln(out, "dry run: nothing written")
		return 0
	}

	if p.Revoke {
		// Only the grant source: a paid or founding row of the account is never touched here.
		if _, err := tx.ExecContext(ctx, `UPDATE entitlements SET status = 'ended', updated_at = now() WHERE user_id = $1 AND source = 'grant'`, uid); err != nil {
			return fail(1, err)
		}
	} else if _, err := tx.ExecContext(ctx, `
		INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', $2)
		ON CONFLICT (user_id, source) DO UPDATE SET status = 'active', current_period_end = EXCLUDED.current_period_end, updated_at = now()`, uid, end); err != nil {
		return fail(1, err)
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fail(1, err)
	}
	var auditID int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO admin_audit (actor_user_id, action, target_type, target_id, target_user_id, details, ip, access_email, outcome, status, finished_at)
		VALUES ($1, $2, 'user', $3, $4, $5::jsonb, '', $6, 'ok', 200, now()) RETURNING id`,
		uuid.Nil, action, uid.String(), uid, string(raw), grantAuditSource).Scan(&auditID); err != nil {
		return fail(1, fmt.Errorf("audit row: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fail(1, err)
	}
	if p.Revoke {
		fmt.Fprintf(out, "revoked the grant of %s (audit row %d). The account reads as free within 15 seconds\n", masked, auditID)
	} else {
		fmt.Fprintf(out, "granted Pro to %s until %s (audit row %d). It shows within 15 seconds\n", masked, end.Format("2006-01-02"), auditID)
	}
	return 0
}

// grantRows reads, and locks, the account's entitlement rows.
func grantRows(ctx context.Context, tx *sql.Tx, uid uuid.UUID) ([]Row, error) {
	rs, err := tx.QueryContext(ctx, `SELECT source, status, current_period_end, updated_at, cancel_at_period_end FROM entitlements WHERE user_id = $1 ORDER BY source FOR UPDATE`, uid)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []Row
	for rs.Next() {
		var r Row
		var end sql.NullTime
		if err := rs.Scan(&r.Source, &r.Status, &end, &r.UpdatedAt, &r.CancelAtPeriodEnd); err != nil {
			return nil, err
		}
		if end.Valid {
			t := end.Time
			r.PeriodEnd = &t
		}
		out = append(out, r)
	}
	return out, rs.Err()
}
