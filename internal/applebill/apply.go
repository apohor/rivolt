package applebill

import (
	"context"
	"database/sql"
	"errors"

	"github.com/google/uuid"
)

// applyDecision writes one decision for the linked account and subscription, inside the caller's transaction (the subscription's link row is
// already locked). It reports false when the event is stale: older than what was applied, or older than a refund that ended Pro.
func applyDecision(ctx context.Context, tx *sql.Tx, uid uuid.UUID, ti TransactionInfo, d Decision, signedDate int64, reversal bool) (bool, error) {
	var last int64
	var revoked sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT last_signed_date, revoked_at FROM billing_apple_subs WHERE original_transaction_id = $1`, ti.OriginalTransactionID).Scan(&last, &revoked); err != nil {
		return false, err
	}
	if signedDate < last {
		return false, nil // events can arrive out of order: only the newest moves the entitlement
	}
	if revoked.Valid && signedDate <= revoked.Int64 && !reversal {
		return false, nil // a refund ended Pro at revoked_at: nothing older may bring it back
	}
	revokedAt := revoked
	switch {
	case d.Revoke:
		revokedAt = sql.NullInt64{Int64: max64(signedDate, revoked.Int64), Valid: true}
	case d.Unrevoke:
		revokedAt = sql.NullInt64{}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE billing_apple_subs SET last_signed_date = $2, revoked_at = $3, updated_at = now() WHERE original_transaction_id = $1`,
		ti.OriginalTransactionID, max64(signedDate, last), revokedAt); err != nil {
		return false, err
	}
	var end sql.NullTime
	if e := ti.Expires(); !e.IsZero() {
		end = sql.NullTime{Time: e, Valid: true}
	}
	var cancel sql.NullBool
	if d.CancelAtPeriodEnd != nil {
		cancel = sql.NullBool{Bool: *d.CancelAtPeriodEnd, Valid: true}
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO entitlements (user_id, source, status, current_period_end, external_id, cancel_at_period_end)
		VALUES ($1, 'apple', $2, $3, $4, COALESCE($5, FALSE))
		ON CONFLICT (user_id, source) DO UPDATE SET status = EXCLUDED.status,
		    current_period_end = COALESCE(EXCLUDED.current_period_end, entitlements.current_period_end),
		    external_id = EXCLUDED.external_id,
		    cancel_at_period_end = COALESCE($5, entitlements.cancel_at_period_end), updated_at = now()`,
		uid, d.Status, end, ti.OriginalTransactionID, cancel)
	if err != nil {
		return false, err
	}
	return true, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

var errNoUser = errors.New("applebill: no such account")
