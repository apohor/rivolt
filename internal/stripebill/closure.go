package stripebill

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// AccountClosure is the Stripe step of every account deletion (self-service, admin and the inactive purge): before the account's rows go,
// its Stripe subscriptions are cancelled at Stripe, so a deleted account is never charged again, and the Stripe customer record (name,
// email, address) is deleted once nothing is open on it (open invoices are voided first, since a deleted account will not pay them; a part-paid invoice or an unresolved dispute holds the record back; Stripe keeps the invoices tax law needs). When Stripe cannot be reached (or this
// server has no Stripe key) the cancel is queued, keyed by the Stripe customer id, and RunRetrier repeats it until it works; it never
// expires, and a backlog is an alert, never a silent skip. Only when the cancel AND the queueing fail does the deletion stop.
//
// The account's customer is read from billing_customers before the cascade removes it. An account that never bought has no customer, and
// there is nothing to do.
type AccountClosure struct {
	DB     *sql.DB
	Client *Client // nil: no Stripe on this server; every cancel is queued until one is configured
	// CapPriceID is the Pro annual Price: its amount (before tax) is the most an open invoice may be and still be voided automatically. Empty or
	// unreadable: nothing is voided, and an open invoice holds the cancel in the queue for a person.
	CapPriceID string
	Log        *slog.Logger
	// Report is told, after each retry pass, how many cancellations are queued and how long the oldest has waited (gauges for the alert).
	Report func(pending int, oldest time.Duration)
}

const (
	cancelTimeout = 10 * time.Second
	retryBatch    = 20
	// alertAfterAttempts: a queued cancel that has failed this many times is logged at ERROR on every attempt.
	alertAfterAttempts = 5
)

func (c *AccountClosure) logger() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// ErrCancelNotQueued is returned when the cancel failed and could not be queued either: the account must not be deleted.
var ErrCancelNotQueued = errors.New("stripe: subscriptions could not be cancelled or queued for cancellation")

// BeforeDelete cancels uid's Stripe subscriptions, or queues the cancel, and returns an error only when neither worked (the caller must not
// delete the account then). nil-safe: without an AccountClosure there is nothing to do.
func (c *AccountClosure) BeforeDelete(ctx context.Context, uid uuid.UUID) error {
	if c == nil || c.DB == nil {
		return nil
	}
	var customer string
	err := c.DB.QueryRowContext(ctx, `SELECT stripe_customer_id FROM billing_customers WHERE user_id = $1`, uid).Scan(&customer)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // never bought through Stripe
	}
	if err != nil {
		return fmt.Errorf("stripe cancel: read customer: %w", err)
	}
	// Remember the customer first: its leftover events (the cancellation itself, a last invoice) are acknowledged, not retried for days.
	if err := db.MarkStripeCustomerClosed(ctx, c.DB, customer); err != nil {
		return fmt.Errorf("stripe cancel: remember customer: %w", err)
	}
	cause := ErrNotConfigured
	if c.Client != nil {
		cctx, cancel := context.WithTimeout(ctx, cancelTimeout)
		var rep CloseReport
		rep, cause = c.Client.CloseCustomer(cctx, customer, c.voidCap(cctx))
		cancel()
		c.audit(rep)
		if cause == nil {
			return nil
		}
	}
	id, qerr := db.RecordStripeCancelFailure(context.WithoutCancel(ctx), c.DB, customer, queueText(cause))
	if qerr != nil {
		c.logger().Error("stripe cancel failed and could not be queued; account not deleted", "err", queueText(cause), "queue_err", qerr.Error())
		return errors.Join(ErrCancelNotQueued, errors.New(queueText(cause)), qerr)
	}
	c.logger().Warn("stripe cancel failed; queued for retry", "failure_id", id.String(), "err", queueText(cause))
	return nil
}

// voidCap reads the Pro annual price for the void cap; without one nothing is voided.
func (c *AccountClosure) voidCap(ctx context.Context) VoidCap {
	if c.Client == nil || c.CapPriceID == "" {
		return VoidCap{}
	}
	p, err := c.Client.Price(ctx, c.CapPriceID)
	if err != nil {
		c.logger().Warn("stripe: cannot read the price that caps automatic invoice voiding; nothing is voided", "err", queueText(err))
		return VoidCap{}
	}
	return VoidCap{Cents: p.UnitAmount, Currency: p.Currency}
}

// audit logs what closing a customer did to its open invoices: one line per void (a void is irreversible) with the Stripe invoice id and the
// amount, and one line per invoice left open and why. Nothing that names a person: no customer id, no email.
func (c *AccountClosure) audit(rep CloseReport) {
	for _, v := range rep.Voided {
		c.logger().Info("stripe: voided an open invoice of a deleted account", "invoice_id", v.ID, "amount_cents", v.AmountCents, "currency", v.Currency)
	}
	for _, l := range rep.Left {
		c.logger().Warn("stripe: an open invoice of a deleted account needs a person", "invoice_id", l.ID, "reason", l.Reason)
	}
}

// queueText is what a failed attempt leaves in the retry queue and the logs: Stripe's status and error code, or a fixed phrase. Never the
// raw error: a transport error or a Stripe message can carry what was sent.
func queueText(err error) string {
	var se *Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &se):
		return se.Error() // "stripe: 500 internal"
	case errors.Is(err, ErrNotConfigured):
		return ErrNotConfigured.Error()
	case errors.Is(err, ErrOpenItems):
		return ErrOpenItems.Error()
	case errors.Is(err, context.DeadlineExceeded):
		return "stripe: timed out"
	}
	return "stripe: request failed"
}

// RunRetrier retries queued cancellations every interval until ctx ends. Safe on every replica (rows are claimed with SKIP LOCKED). No-op
// without a database.
func (c *AccountClosure) RunRetrier(ctx context.Context, interval time.Duration) {
	if c == nil || c.DB == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.RetryOnce(ctx)
		}
	}
}

// RetryOnce runs one pass: retry what is due, purge old tombstones, report the backlog.
func (c *AccountClosure) RetryOnce(ctx context.Context) {
	if c.Client != nil {
		due, err := db.ClaimDueStripeCancels(ctx, c.DB, retryBatch)
		if err != nil {
			c.logger().Warn("stripe cancel retry: claim failed", "err", err.Error())
		}
		for _, it := range due {
			cctx, cancel := context.WithTimeout(ctx, cancelTimeout)
			rep, cerr := c.Client.CloseCustomer(cctx, it.Customer, c.voidCap(cctx))
			cancel()
			c.audit(rep)
			if cerr != nil {
				if err := db.StripeCancelFailedAgain(ctx, c.DB, it.ID, queueText(cerr)); err != nil {
					c.logger().Warn("stripe cancel retry: record attempt failed", "failure_id", it.ID.String(), "err", err.Error())
				}
				if it.Attempts+1 >= alertAfterAttempts {
					c.logger().Error("stripe cancel for a deleted account keeps failing; the person may still be billed", "failure_id", it.ID.String(), "attempts", it.Attempts+1)
				}
				continue
			}
			if err := db.ResolveIdPDeleteFailure(ctx, c.DB, it.ID); err == nil {
				c.logger().Info("stripe cancel retry: resolved", "failure_id", it.ID.String(), "attempts", it.Attempts)
			}
		}
	}
	if _, err := db.PurgeClosedStripeCustomers(ctx, c.DB); err != nil {
		c.logger().Warn("stripe cancel: purge tombstones failed", "err", err.Error())
	}
	if pending, oldest, err := db.StripeCancelBacklog(ctx, c.DB); err != nil {
		c.logger().Warn("stripe cancel: backlog read failed", "err", err.Error())
	} else {
		if c.Report != nil {
			c.Report(pending, oldest)
		}
		if pending > 0 && c.Client == nil {
			c.logger().Error("queued Stripe cancellations cannot be retried: no Stripe key on this server", "pending", pending)
		}
	}
}
