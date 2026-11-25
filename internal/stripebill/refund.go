package stripebill

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
)

// RefundWindow is how long after a payment its payer can refund it themselves. The window is inclusive: a payment made exactly 14 days ago
// can still be refunded.
const RefundWindow = 14 * 24 * time.Hour

// Why a self-serve refund is not on offer. These are the codes the API returns and the Plan card reads.
const (
	RefundNoAccount    = "no_billing_account" // the account has no Stripe customer
	RefundNoPayment    = "no_payment"         // no paid, unrefunded payment on the customer
	RefundWindowClosed = "window_closed"      // the newest payment is older than RefundWindow
	RefundUsed         = "used"               // the account's one self-serve refund is spent
)

// ErrRefundUnavailable is returned by SelfRefund when no refund is on offer; the Offer says why.
var ErrRefundUnavailable = errors.New("stripe: no self-serve refund is available")

// Payment is the part of a Stripe Charge the refund flow reads.
type Payment struct {
	ID             string `json:"id"`
	Created        int64  `json:"created"`
	Amount         int64  `json:"amount"`
	AmountRefunded int64  `json:"amount_refunded"`
	Currency       string `json:"currency"`
	Paid           bool   `json:"paid"`
	Refunded       bool   `json:"refunded"`
	Disputed       bool   `json:"disputed"`
	Status         string `json:"status"`
}

// refundable: paid, nothing refunded yet (a partly refunded payment is the operator's), not under dispute.
func (p Payment) refundable() bool {
	return p.Paid && p.Status == "succeeded" && !p.Refunded && p.AmountRefunded == 0 && !p.Disputed && p.Amount > 0
}

// RecentPayments lists a customer's newest charges (Stripe returns them newest first).
func (c *Client) RecentPayments(ctx context.Context, customer string) ([]Payment, error) {
	var list struct {
		Data []Payment `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/charges?limit=20&customer="+url.QueryEscape(customer), nil, "", &list); err != nil {
		return nil, err
	}
	return list.Data, nil
}

// RefundPayment refunds a whole charge. idem is the Idempotency-Key: the same key returns the same refund, never a second one.
func (c *Client) RefundPayment(ctx context.Context, charge, idem string) (refundID string, err error) {
	var out struct {
		ID string `json:"id"`
	}
	form := url.Values{"charge": {charge}, "reason": {"requested_by_customer"}, "metadata[source]": {"self_serve"}}
	if err := c.do(ctx, http.MethodPost, "/v1/refunds", form, idem, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", errors.New("stripe: the refund has no id")
	}
	return out.ID, nil
}

// RefundOffer is what the Plan card shows: whether a refund is on offer, until when, and how much.
type RefundOffer struct {
	Eligible    bool
	Reason      string // why not, when Eligible is false
	Until       time.Time
	AmountCents int64
	Currency    string
	charge      string
}

// refundKey is the Idempotency-Key of an account's one self-serve refund.
func refundKey(uid uuid.UUID) string { return "rivolt-self-refund-" + uid.String() }

func (s *Service) pendingRefund(ctx context.Context, uid uuid.UUID) (charge string, state string, amount int64, currency string, err error) {
	err = s.DB.QueryRowContext(ctx, `SELECT charge_id, state, amount_cents, currency FROM billing_self_refunds WHERE user_id = $1`, uid).Scan(&charge, &state, &amount, &currency)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", 0, "", nil
	}
	return
}

// RefundOffer reads whether the account can refund itself now: it has a Stripe customer, has not used its one self-serve refund, and the
// newest paid payment is at most RefundWindow old. It asks Stripe, so a caller should not poll it. It does not depend on RIVOLT_BILLING
// (like the Portal: a person who paid can always get the money back).
func (s *Service) RefundOffer(ctx context.Context, uid uuid.UUID) (RefundOffer, error) {
	cust, err := s.customerOf(ctx, uid)
	if err != nil {
		return RefundOffer{}, err
	}
	if cust == "" {
		return RefundOffer{Reason: RefundNoAccount}, nil
	}
	_, state, _, _, err := s.pendingRefund(ctx, uid)
	if err != nil {
		return RefundOffer{}, err
	}
	if state == "done" {
		return RefundOffer{Reason: RefundUsed}, nil
	}
	pays, err := s.Client.RecentPayments(ctx, cust)
	if err != nil {
		return RefundOffer{}, err
	}
	for _, p := range pays {
		if !p.refundable() {
			continue
		}
		until := time.Unix(p.Created, 0).UTC().Add(RefundWindow)
		if s.now().After(until) {
			return RefundOffer{Reason: RefundWindowClosed}, nil
		}
		return RefundOffer{Eligible: true, Until: until, AmountCents: p.Amount, Currency: p.Currency, charge: p.ID}, nil
	}
	return RefundOffer{Reason: RefundNoPayment}, nil
}

// SelfRefund refunds the account's newest payment in full, ends its Pro at once and records it. At most one per account, ever: the row
// reserved in billing_self_refunds before Stripe is called is the limit, and a repeat (a double tap, a retry after a lost answer) finds it
// and sends the same Idempotency-Key, so it returns the same refund. Pro ends in the same transaction that marks the row done; the
// charge.refunded webhook that follows runs the same steps again and changes nothing.
func (s *Service) SelfRefund(ctx context.Context, uid uuid.UUID) (RefundOffer, error) {
	cust, err := s.customerOf(ctx, uid)
	if err != nil {
		return RefundOffer{}, err
	}
	if cust == "" {
		return RefundOffer{Reason: RefundNoAccount}, ErrRefundUnavailable
	}
	charge, state, amount, currency, err := s.pendingRefund(ctx, uid)
	if err != nil {
		return RefundOffer{}, err
	}
	switch state {
	case "done":
		return RefundOffer{Reason: RefundUsed}, ErrRefundUnavailable
	case "pending":
		// A tap that did not finish: complete the same refund (same charge, same key). The window was checked when it was reserved.
	default:
		offer, err := s.RefundOffer(ctx, uid)
		if err != nil {
			return RefundOffer{}, err
		}
		if !offer.Eligible {
			return offer, ErrRefundUnavailable
		}
		res, err := s.DB.ExecContext(ctx, `
			INSERT INTO billing_self_refunds (user_id, charge_id, amount_cents, currency) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, uid, offer.charge, offer.AmountCents, offer.Currency)
		if err != nil {
			return RefundOffer{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Lost a race with another tap (or the charge is already claimed): read what is there and carry on as a retry of it.
			charge, state, amount, currency, err = s.pendingRefund(ctx, uid)
			if err != nil {
				return RefundOffer{}, err
			}
			if state == "done" || state == "" {
				return RefundOffer{Reason: RefundUsed}, ErrRefundUnavailable
			}
		} else {
			charge, amount, currency = offer.charge, offer.AmountCents, offer.Currency
		}
	}
	refundID, err := s.Client.RefundPayment(ctx, charge, refundKey(uid))
	if err != nil {
		var se *Error
		if errors.As(err, &se) && se.Code == "charge_already_refunded" {
			// The charge was refunded already (the first answer was lost and Stripe's key expired, or a refund from the Dashboard): that
			// is this account's refund. Record it as spent and end Pro; the account does not get a second one.
			if ferr := s.finishRefund(ctx, uid, cust, ""); ferr != nil {
				return RefundOffer{}, ferr
			}
			return RefundOffer{Reason: RefundUsed}, ErrRefundUnavailable
		}
		if errors.As(err, &se) && se.Status >= 400 && se.Status < 500 && se.Status != http.StatusTooManyRequests && se.Status != http.StatusConflict {
			// Stripe refuses this refund for good (for example the charge is already refunded; a 409 is another tap's call still in flight, and a 429 a limit: both retry): free the account's one refund.
			if _, derr := s.DB.ExecContext(ctx, `DELETE FROM billing_self_refunds WHERE user_id = $1 AND state = 'pending'`, uid); derr != nil {
				slog.Warn("self-serve refund: releasing the reservation failed", "err", derr.Error())
			}
			return RefundOffer{Reason: RefundNoPayment}, ErrRefundUnavailable
		}
		return RefundOffer{}, err // Stripe or the network failed: the reservation stays, a retry sends the same key
	}
	if err := s.finishRefund(ctx, uid, cust, refundID); err != nil {
		return RefundOffer{}, err
	}
	return RefundOffer{AmountCents: amount, Currency: currency}, nil
}

// cancelAfterRefund cancels the customer's Stripe subscriptions now, as the webhook does after a refund; a failure is left to the queued
// cancel (stripe_cancel_retrier), which endProForRefund already queued.
func (s *Service) cancelAfterRefund(ctx context.Context, customer string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := s.Client.CancelCustomerSubscriptions(cctx, customer); err != nil {
		slog.Warn("self-serve refund: cancelling the subscription failed; queued for retry", "err", queueText(err))
		return
	}
	if err := db.ResolveStripeRefundCancel(cctx, s.DB, customer); err != nil {
		slog.Warn("self-serve refund: clearing the queued cancel failed", "err", err.Error())
	}
}

// finishRefund marks the account's reservation done (refundID is empty when Stripe reported the charge already refunded), ends Pro in the
// same transaction, then cancels the subscriptions.
func (s *Service) finishRefund(ctx context.Context, uid uuid.UUID, cust, refundID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE billing_self_refunds SET state = 'done', refund_id = NULLIF($2, ''), done_at = now() WHERE user_id = $1`, uid, refundID); err != nil {
		return err
	}
	if err := endProForRefund(ctx, tx, uid, cust, s.now().Unix()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cancelAfterRefund(ctx, cust)
	return nil
}
