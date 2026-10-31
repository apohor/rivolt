package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// StripeCancelItem is a queued Stripe cancellation: the customer whose subscriptions must be cancelled.
type StripeCancelItem struct {
	ID       uuid.UUID
	Customer string
	Attempts int
}

func stripeQueueKey(customer string) string { return "stripe:" + customer }

// RecordStripeCancelFailure queues (or, for a customer already queued, counts another failed attempt of) the cancellation of a deleted
// account's Stripe subscriptions. errMsg must not contain a key. The row names the Stripe customer only.
func RecordStripeCancelFailure(ctx context.Context, d *sql.DB, customer, errMsg string) (uuid.UUID, error) {
	if d == nil || !strings.HasPrefix(customer, "cus_") {
		return uuid.Nil, errors.New("db: stripe cancel queue needs a database and a Stripe customer id")
	}
	var id uuid.UUID
	err := d.QueryRowContext(ctx, `
		INSERT INTO idp_delete_failures (username, error, kind)
		VALUES ($1, $2, 'stripe')
		ON CONFLICT (lower(username)) DO UPDATE
		   SET error = EXCLUDED.error,
		       attempts = idp_delete_failures.attempts + 1,
		       last_tried = NOW()
		RETURNING id`, stripeQueueKey(customer), errMsg).Scan(&id)
	return id, err
}

// ClaimDueStripeCancels returns up to limit queued cancellations whose backoff has elapsed (5 min doubling per attempt, capped at ONE HOUR:
// a subscription still billing a deleted person is not something to wait a day on) and stamps them as tried now, so another replica skips
// them.
func ClaimDueStripeCancels(ctx context.Context, d *sql.DB, limit int) ([]StripeCancelItem, error) {
	rows, err := d.QueryContext(ctx, `
		UPDATE idp_delete_failures f SET last_tried = NOW()
		 WHERE f.id IN (
		     SELECT id FROM idp_delete_failures
		      WHERE kind = 'stripe'
		        AND last_tried <= NOW() - LEAST(interval '5 minutes' * power(2, LEAST(GREATEST(attempts - 1, 0), 9)), interval '1 hour')
		      ORDER BY last_tried
		      LIMIT $1
		      FOR UPDATE SKIP LOCKED)
		 RETURNING f.id, f.username, f.attempts`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StripeCancelItem
	for rows.Next() {
		var it StripeCancelItem
		var key string
		if err := rows.Scan(&it.ID, &key, &it.Attempts); err != nil {
			return nil, err
		}
		it.Customer = strings.TrimPrefix(key, "stripe:")
		out = append(out, it)
	}
	return out, rows.Err()
}

// StripeCancelFailedAgain counts another failed attempt for a queued cancellation.
func StripeCancelFailedAgain(ctx context.Context, d *sql.DB, id uuid.UUID, errMsg string) error {
	_, err := d.ExecContext(ctx, `
		UPDATE idp_delete_failures SET attempts = attempts + 1, error = $2, last_tried = NOW()
		 WHERE id = $1 AND kind = 'stripe'`, id, errMsg)
	return err
}

// StripeCancelBacklog is how many cancellations are still queued and how long the oldest has waited: what the alert watches.
func StripeCancelBacklog(ctx context.Context, d *sql.DB) (pending int, oldest time.Duration, err error) {
	var secs sql.NullFloat64
	err = d.QueryRowContext(ctx, `
		SELECT count(*), EXTRACT(EPOCH FROM now() - min(failed_at)) FROM idp_delete_failures WHERE kind = 'stripe'`).Scan(&pending, &secs)
	if secs.Valid {
		oldest = time.Duration(secs.Float64 * float64(time.Second))
	}
	return pending, oldest, err
}

// MarkStripeCustomerClosed remembers that the customer belonged to a deleted account (so its leftover events are acknowledged).
func MarkStripeCustomerClosed(ctx context.Context, d *sql.DB, customer string) error {
	_, err := d.ExecContext(ctx, `INSERT INTO stripe_closed_customers (customer_id) VALUES ($1) ON CONFLICT (customer_id) DO NOTHING`, customer)
	return err
}

// PurgeClosedStripeCustomers drops tombstones older than 30 days (Stripe stops retrying a failed event after about three).
func PurgeClosedStripeCustomers(ctx context.Context, d *sql.DB) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM stripe_closed_customers WHERE closed_at < now() - interval '30 days'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
