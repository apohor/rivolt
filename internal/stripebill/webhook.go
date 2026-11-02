package stripebill

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/entitlements"
)

// MaxBodyBytes caps a webhook body: Stripe events are a few KB.
const MaxBodyBytes = 1 << 20

// MetadataUserID is the key Rivolt puts the account's id under in a subscription's metadata (Checkout's subscription_data.metadata, part
// b), so a subscription event resolves its account on its own, whatever order Stripe delivers the events in.
const MetadataUserID = "rivolt_user_id"

// EventRetention is how long event ids are kept: Stripe retries a failed delivery for about three days, so a month is ample.
const EventRetention = 30 * 24 * time.Hour

// PurgeEvents deletes event ids older than the retention (the existing retention job calls it daily).
func PurgeEvents(ctx context.Context, d *sql.DB) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM stripe_events WHERE received_at < now() - make_interval(secs => $1)`, EventRetention.Seconds())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ErrUnmapped is an event for an account Rivolt cannot find (yet). It is answered 500 so Stripe retries for days: Stripe does not order
// its events, and a subscription event can arrive before the checkout.session.completed that links its customer.
var ErrUnmapped = errors.New("stripe: no account for the event yet")

// Handler is POST /api/billing/stripe/webhook. It verifies the signature, then applies the event in ONE transaction that also records the
// event id, so a retried or replayed delivery changes nothing, and a delivery that fails halfway is rolled back whole (claim included) and
// answered 500 so Stripe retries it. Entitlements are written only here (and by the founding grant, the trial and admin grants).
type Handler struct {
	DB     *sql.DB // the system pool: the webhook has no signed-in user
	Secret string  // the endpoint's signing secret
	// ObserveLag is told how long after Stripe created an event it was received (the event's own created time against this server's clock).
	// OnBilling is told a billing moment an operator may want to see ("payment_failed": a subscription went past due). Counts only.
	ObserveLag func(time.Duration)
	OnBilling  func(kind string)
	// Invalidate drops the account's cached plan on this pod (the others see it within the plan cache's TTL).
	Invalidate func(uuid.UUID)
	Now        func() time.Time
	Log        *slog.Logger
	// Observe, when set, is told each event's outcome: applied, duplicate, ignored (a type or state that changes nothing), unmapped (no
	// account for the object), stale (older than what was applied), or rejected (bad signature). A fixed vocabulary; no user data.
	Observe func(eventType, outcome string)
	// Countries reads a customer's billing country for the US-only check on subscription changes (nil: only Checkout's own country is
	// checked). OnNonUS is told each billing country that is not the US, the code only: for the counter. Neither ever changes what the
	// event does: a non-US account still gets Pro, and the operator is told (see checkCountry).
	Countries interface {
		CustomerCountry(ctx context.Context, customer string) (string, error)
	}
	OnNonUS func(country string)
	// seen remembers the (customer, country) pairs already reported by this process, so a customer whose subscription changes every month is
	// reported once, not at each event.
	seen sync.Map
}

// Outcomes.
const (
	OutcomeApplied   = "applied"
	OutcomeDuplicate = "duplicate"
	OutcomeIgnored   = "ignored"
	OutcomeUnmapped  = "unmapped"
	OutcomeStale     = "stale"
	OutcomeRejected  = "rejected"
)

// MetricTypes are the event types the endpoint handles, as the metric labels them; any other type is "other", and a delivery rejected before
// its type is known is "none". MetricOutcomes are the outcomes. Both are fixed sets, so the label cardinality is bounded by this package.
var (
	MetricTypes    = []string{"checkout.session.completed", "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted", "charge.refunded", "other", "none"}
	MetricOutcomes = []string{OutcomeApplied, OutcomeDuplicate, OutcomeIgnored, OutcomeUnmapped, OutcomeStale, OutcomeRejected}
)

// MetricType is the label for an event type.
func MetricType(t string) string {
	if t == "" {
		return "none"
	}
	for _, k := range MetricTypes {
		if k == t {
			return t
		}
	}
	return "other"
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) observe(typ, outcome string) {
	if h.Observe != nil {
		h.Observe(typ, outcome)
	}
}

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	if err := VerifySignature(body, r.Header.Get("Stripe-Signature"), h.Secret, h.now(), SignatureTolerance); err != nil {
		h.observe("", OutcomeRejected)
		if h.Log != nil {
			h.Log.Warn("stripe webhook: signature rejected", "err", err.Error())
		}
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	var ev Event
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" || ev.Type == "" {
		http.Error(w, "bad event", http.StatusBadRequest)
		return
	}
	if h.ObserveLag != nil && ev.Created > 0 {
		h.ObserveLag(h.now().Sub(time.Unix(ev.Created, 0)))
	}
	outcome, uid, err := h.apply(r.Context(), ev)
	if err != nil {
		if h.Log != nil {
			h.Log.Error("stripe webhook: could not apply the event; Stripe will retry", "event", ev.ID, "type", ev.Type, "err", err.Error())
		}
		http.Error(w, "retry", http.StatusInternalServerError)
		return
	}
	if uid != uuid.Nil && h.Invalidate != nil {
		h.Invalidate(uid)
	}
	h.observe(ev.Type, outcome)
	h.checkCountry(r.Context(), ev, outcome)
	w.WriteHeader(http.StatusOK)
}

// apply records the event and applies it, in one transaction. It returns the outcome and the account whose plan changed (uuid.Nil: none).
func (h *Handler) apply(ctx context.Context, ev Event) (string, uuid.UUID, error) {
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", uuid.Nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx, `INSERT INTO stripe_events (event_id, type) VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`, ev.ID, ev.Type)
	if err != nil {
		return "", uuid.Nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return OutcomeDuplicate, uuid.Nil, nil
	}
	var outcome string
	var uid uuid.UUID
	switch ev.Type {
	case "checkout.session.completed":
		outcome, err = h.checkoutCompleted(ctx, tx, ev)
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted":
		outcome, uid, err = h.subscriptionChanged(ctx, tx, ev)
	case "charge.refunded":
		outcome, uid, err = h.chargeRefunded(ctx, tx, ev)
	default:
		outcome = OutcomeIgnored
	}
	if err != nil {
		return "", uuid.Nil, err
	}
	return outcome, uid, tx.Commit()
}

// checkoutCompleted links the account that started the Checkout (client_reference_id, set by Rivolt) to its Stripe customer. The plan itself
// comes from the subscription events.
func (h *Handler) checkoutCompleted(ctx context.Context, tx *sql.Tx, ev Event) (string, error) {
	var cs CheckoutSession
	if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
		return "", err
	}
	uid, err := uuid.Parse(cs.ClientReferenceID)
	if cs.Mode != "subscription" || cs.Customer == "" || err != nil {
		return OutcomeIgnored, nil
	}
	ok, err := linkCustomer(ctx, tx, uid, cs.Customer)
	if errors.Is(err, errCustomerOwned) {
		h.warnCustomerOwned(ev)
		return OutcomeIgnored, nil
	}
	if err != nil {
		return "", err
	}
	if !ok {
		return OutcomeUnmapped, nil
	}
	return OutcomeApplied, nil
}

// errCustomerOwned: the Stripe customer is already another account's. Retrying cannot change that, so the event is acknowledged, not 500ed.
var errCustomerOwned = errors.New("stripe: customer belongs to another account")

func (h *Handler) warnCustomerOwned(ev Event) {
	if h.Log != nil {
		h.Log.Warn("stripe webhook: event names a customer that belongs to another account; ignored", "event", ev.ID, "type", ev.Type)
	}
}

// linkCustomer records uid's Stripe customer. false: no such account (a deleted one), nothing recorded.
func linkCustomer(ctx context.Context, tx *sql.Tx, uid uuid.UUID, customer string) (bool, error) {
	// stripe_customer_id is UNIQUE: a customer that is another account's would fail the insert with a violation, a 500 Stripe retries for days.
	var other uuid.UUID
	switch err := tx.QueryRowContext(ctx, `SELECT user_id FROM billing_customers WHERE stripe_customer_id = $1 AND user_id <> $2`, customer, uid).Scan(&other); {
	case err == nil:
		return false, errCustomerOwned
	case !errors.Is(err, sql.ErrNoRows):
		return false, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO billing_customers (user_id, stripe_customer_id)
		SELECT id, $2 FROM users WHERE id = $1
		ON CONFLICT (user_id) DO UPDATE SET stripe_customer_id = EXCLUDED.stripe_customer_id, updated_at = now()`, uid, customer)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// accountFor finds the account of a Stripe object: the user id Rivolt put in the subscription's metadata, else the customer on record.
func accountFor(ctx context.Context, tx *sql.Tx, metadata map[string]string, customer string) (uuid.UUID, error) {
	if raw := metadata[MetadataUserID]; raw != "" {
		if uid, err := uuid.Parse(raw); err == nil {
			var ok bool
			if err := tx.QueryRowContext(ctx, `SELECT true FROM users WHERE id = $1`, uid).Scan(&ok); err == nil && ok {
				return uid, nil
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return uuid.Nil, err
			}
		}
	}
	if customer == "" {
		return uuid.Nil, nil
	}
	var uid uuid.UUID
	err := tx.QueryRowContext(ctx, `SELECT user_id FROM billing_customers WHERE stripe_customer_id = $1`, customer).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, nil
	}
	return uid, err
}

// customerClosed reports whether the Stripe customer belonged to an account that was deleted (stripe_closed_customers).
func customerClosed(ctx context.Context, tx *sql.Tx, customer string) (bool, error) {
	if customer == "" {
		return false, nil
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM stripe_closed_customers WHERE customer_id = $1`, customer).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// subscriptionChanged applies a subscription's state to the account's stripe entitlement.
func (h *Handler) subscriptionChanged(ctx context.Context, tx *sql.Tx, ev Event) (string, uuid.UUID, error) {
	var sub Subscription
	if err := json.Unmarshal(ev.Data.Object, &sub); err != nil {
		return "", uuid.Nil, err
	}
	status, ok := EntitlementStatus(sub.Status)
	if ev.Type == "customer.subscription.deleted" {
		status, ok = entitlements.StatusEnded, true
	}
	end := sub.PeriodEnd()
	if sub.ID == "" || !ok || end.IsZero() {
		return OutcomeIgnored, uuid.Nil, nil
	}
	uid, err := accountFor(ctx, tx, sub.Metadata, sub.Customer)
	if err != nil {
		return "", uuid.Nil, err
	}
	if uid == uuid.Nil {
		// The customer of an account that was deleted (its subscriptions were cancelled by the deletion): nothing to apply, and retrying
		// for days would only get the endpoint switched off. Acknowledged.
		if closed, err := customerClosed(ctx, tx, sub.Customer); err != nil {
			return "", uuid.Nil, err
		} else if closed {
			return OutcomeIgnored, uuid.Nil, nil
		}
		// Not acknowledged: the whole delivery (claim included) rolls back and Stripe retries, by which time the checkout event that
		// links the customer has usually been applied. A subscription that is not Rivolt's keeps retrying until Stripe gives up.
		if h.Log != nil {
			h.Log.Warn("stripe webhook: subscription event for no known account yet; Stripe will retry", "event", ev.ID, "subscription", sub.ID)
		}
		h.observe(ev.Type, OutcomeUnmapped)
		return "", uuid.Nil, ErrUnmapped
	}
	if sub.Customer != "" {
		if _, err := linkCustomer(ctx, tx, uid, sub.Customer); errors.Is(err, errCustomerOwned) {
			h.warnCustomerOwned(ev)
			return OutcomeIgnored, uuid.Nil, nil
		} else if err != nil {
			return "", uuid.Nil, err
		}
	}
	// A full refund ended this account's Pro at revoked_at: nothing older than that may bring it back.
	var revoked sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT revoked_at FROM billing_customers WHERE user_id = $1`, uid).Scan(&revoked); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", uuid.Nil, err
	}
	if revoked.Valid && ev.Created <= revoked.Int64 {
		return OutcomeStale, uuid.Nil, nil
	}
	// Events can arrive out of order: only the newest one per subscription moves the entitlement.
	interval, amount, currency := sub.Terms()
	res, err := tx.ExecContext(ctx, `
		INSERT INTO billing_stripe_subs (subscription_id, user_id, last_event_created, price_interval, unit_amount, currency, trialing)
		VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5::bigint, 0), NULLIF($6, ''), $7)
		ON CONFLICT (subscription_id) DO UPDATE SET last_event_created = EXCLUDED.last_event_created, updated_at = now(),
		    price_interval = COALESCE(EXCLUDED.price_interval, billing_stripe_subs.price_interval),
		    unit_amount    = COALESCE(EXCLUDED.unit_amount, billing_stripe_subs.unit_amount),
		    currency       = COALESCE(EXCLUDED.currency, billing_stripe_subs.currency),
		    trialing       = EXCLUDED.trialing
		 WHERE billing_stripe_subs.last_event_created <= EXCLUDED.last_event_created`,
		sub.ID, uid, ev.Created, interval, amount, currency, sub.Status == "trialing")
	if err != nil {
		return "", uuid.Nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return OutcomeStale, uuid.Nil, nil
	}
	// payment_failed is the TRANSITION into past due, once per failure: Stripe sends several subscription.updated events while it retries the
	// invoice, and each would otherwise count. The previous status is read here, in the same transaction as the write below.
	var before sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status FROM entitlements WHERE user_id = $1 AND source = 'stripe'`, uid).Scan(&before); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", uuid.Nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO entitlements (user_id, source, status, current_period_end, external_id, cancel_at_period_end)
		VALUES ($1, 'stripe', $2, $3, $4, $5)
		ON CONFLICT (user_id, source) DO UPDATE SET status = EXCLUDED.status, current_period_end = EXCLUDED.current_period_end,
		    external_id = EXCLUDED.external_id, cancel_at_period_end = EXCLUDED.cancel_at_period_end, updated_at = now()`,
		uid, status, end, sub.ID, sub.WillCancel() && status == entitlements.StatusActive); err != nil {
		return "", uuid.Nil, err
	}
	if status == entitlements.StatusPastDue && before.String != entitlements.StatusPastDue && ev.Type != "customer.subscription.deleted" && h.OnBilling != nil {
		h.OnBilling("payment_failed")
	}
	return OutcomeApplied, uid, nil
}

// chargeRefunded ends the account's Stripe Pro after a full refund (a refund of the whole charge: Pro is not kept on a returned payment).
// A partial refund changes nothing. The Stripe subscription itself stays active unless the operator also cancels it, and its next renewal
// event then grants Pro again (correctly: they would be paying again), so the operator's refund procedure must cancel the subscription too.
func (h *Handler) chargeRefunded(ctx context.Context, tx *sql.Tx, ev Event) (string, uuid.UUID, error) {
	var ch Charge
	if err := json.Unmarshal(ev.Data.Object, &ch); err != nil {
		return "", uuid.Nil, err
	}
	if !ch.Refunded || ch.Customer == "" {
		return OutcomeIgnored, uuid.Nil, nil
	}
	uid, err := accountFor(ctx, tx, nil, ch.Customer)
	if err != nil {
		return "", uuid.Nil, err
	}
	if uid == uuid.Nil {
		if closed, err := customerClosed(ctx, tx, ch.Customer); err != nil {
			return "", uuid.Nil, err
		} else if closed {
			return OutcomeIgnored, uuid.Nil, nil
		}
		h.observe(ev.Type, OutcomeUnmapped)
		return "", uuid.Nil, ErrUnmapped
	}
	if _, err := tx.ExecContext(ctx, `UPDATE billing_customers SET revoked_at = GREATEST(COALESCE(revoked_at, 0), $2), updated_at = now() WHERE user_id = $1`, uid, ev.Created); err != nil {
		return "", uuid.Nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE entitlements SET status = 'ended', cancel_at_period_end = FALSE, updated_at = now()
		 WHERE user_id = $1 AND source = 'stripe'`, uid); err != nil {
		return "", uuid.Nil, err
	}
	return OutcomeApplied, uid, nil
}

// checkCountry is the US-only guard: Rivolt Pro is sold in the United States only. After an event has been applied it looks at the billing
// country: on checkout.session.completed the one the customer entered at Checkout, on customer.subscription.updated the customer's address,
// read from Stripe. An address changed later in the Portal emits customer.updated, which is not subscribed to, so that change is caught at
// the next subscription change (a plan change, or the renewal at the latest). A country other than the US is reported (a WARN with the
// country code and the opaque Stripe customer id, never the address or any name or email, and OnNonUS for the counter) and NOTHING else: the account keeps the Pro it paid for, because refusing
// silently is worse; the operator refunds in full, cancels and writes to the customer (docs/launch-flip.md in rivolt-infra). Best effort: a
// failure to read the country is not the event's failure.
func (h *Handler) checkCountry(ctx context.Context, ev Event, outcome string) {
	if outcome != OutcomeApplied {
		return
	}
	var customer, country string
	switch ev.Type {
	case "checkout.session.completed":
		var cs CheckoutSession
		if json.Unmarshal(ev.Data.Object, &cs) != nil {
			return
		}
		customer, country = cs.Customer, strings.ToUpper(strings.TrimSpace(cs.CustomerDetails.Address.Country))
	case "customer.subscription.updated":
		var sub Subscription
		if json.Unmarshal(ev.Data.Object, &sub) != nil || sub.Customer == "" || h.Countries == nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		c, err := h.Countries.CustomerCountry(cctx, sub.Customer)
		if err != nil {
			return
		}
		customer, country = sub.Customer, c
	default:
		return
	}
	if country == "" || country == "US" {
		return
	}
	key := customer + "/" + country
	if _, dup := h.seen.LoadOrStore(key, true); dup {
		return
	}
	if h.OnNonUS != nil {
		h.OnNonUS(country)
	}
	if h.Log != nil {
		// The customer id is an opaque Stripe id (no name, email or address): the operator finds the customer by it, since Stripe's search
		// does not filter by address country.
		h.Log.Warn("stripe webhook: a billing address outside the US; the account keeps Pro, the operator refunds and cancels", "country", country, "customer", customer, "event_type", ev.Type)
	}
}
