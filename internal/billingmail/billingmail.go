// Package billingmail sends Rivolt Pro's transactional billing emails for Stripe subscriptions: the purchase confirmation (the renewal terms
// and how to cancel, which Stripe's own receipt does not carry), the payment-failed notice during the 7-day grace (when it ends, how to update
// the card), and the source of the yearly-renewal reminder (internal/launchnotice).
//
// They are service emails: they go out with engagement emails off, outside the engagement budget, to an account's verified address. Each is
// sent once per event: a row in account_notices per (account, kind) is claimed, sent and marked (the same machinery as the launch notice),
// the kind carrying what the email is about (the subscription id, the failed period), so a replay, a second replica or a restart cannot send
// it twice and a failed send is retried by the next run. The job reads the state the Stripe webhook wrote (entitlements and
// billing_stripe_subs), so a webhook that succeeded but whose email failed is caught up; nothing here calls Stripe.
//
// Links go to the fixed Plan card (launchnotice.PlanURL), which opens the billing portal after sign-in: never a pre-made portal session URL,
// which would work as a bearer link to the customer's billing page if the email were forwarded.
package billingmail

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/launchnotice"
)

// Kinds, for the metric (rivolt_dated_notices_total{kind,outcome}) and the notice rows.
const (
	KindPurchase      = launchnotice.KindBillingPurchase
	KindPaymentFailed = launchnotice.KindBillingPaymentFailed
)

// RecentFor is how far back a subscription event still triggers a purchase confirmation: a subscription last touched longer ago than this
// is older than the job (or a long outage) and is not announced as new.
const RecentFor = 7 * 24 * time.Hour

// Mailer is what the job sends through.
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// Stats is one run's outcome.
type Stats struct{ Considered, Sent, Failed, Skipped int }

// Job sends the purchase confirmations and payment-failed notices.
type Job struct {
	DB      *sql.DB // the system pool
	Mailer  Mailer
	Now     func() time.Time
	Logger  *slog.Logger
	Skip    map[uuid.UUID]bool // the demo and bypass accounts
	Observe func(kind, outcome string)
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) log() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

func (j *Job) observe(kind, outcome string) {
	if j.Observe != nil {
		j.Observe(kind, outcome)
	}
}

// Purchase is a subscription to confirm.
type Purchase struct {
	UserID       uuid.UUID
	Email        string
	Subscription string
	PeriodEnd    time.Time // the next renewal, or the first charge while trialing
	Interval     string    // month or year
	Amount       int64     // minor units
	Currency     string
	Trialing     bool
}

// Failure is a renewal that failed and is in its grace.
type Failure struct {
	UserID    uuid.UUID
	Email     string
	PeriodEnd time.Time // the period that ended unpaid; grace runs from here
}

// PurchaseKind and FailureKind are the notice kinds: one confirmation per subscription, one failed-payment notice per failed period.
func PurchaseKind(subscription string) string { return "billing_purchase_" + subscription }
func FailureKind(periodEnd time.Time) string {
	return "billing_failed_" + periodEnd.UTC().Format("20060102")
}

// PurchaseParams and FailureParams are what the emails state (stored with the notice row, so a retry sends the same text).
type PurchaseParams struct {
	Interval string `json:"interval"`
	Amount   string `json:"amount"` // "$34.99"
	Price    string `json:"price"`  // "$34.99 a year"
	Date     string `json:"date"`   // YYYY-MM-DD: the renewal, or the first charge
	Trialing bool   `json:"trialing"`
}
type FailureParams struct {
	GraceEnds string `json:"grace_ends"` // YYYY-MM-DD
}

// Amount is "$34.99".
func Amount(amount int64, currency string) string {
	sym := map[string]string{"usd": "$", "eur": "€", "gbp": "£"}[strings.ToLower(currency)]
	if sym == "" {
		sym = strings.ToUpper(currency) + " "
	}
	return fmt.Sprintf("%s%d.%02d", sym, amount/100, amount%100)
}

// PriceLabel is "$34.99 a year".
func PriceLabel(amount int64, currency, interval string) string {
	per := map[string]string{"month": "a month", "year": "a year"}[interval]
	if per == "" {
		per = "per " + interval
	}
	return Amount(amount, currency) + " " + per
}

// RunOnce sends what is due. Each send is claimed first; a failed send releases its claim for the next run.
func (j *Job) RunOnce(ctx context.Context) (Stats, error) {
	var st Stats
	now := j.now()
	ps, err := j.purchases(ctx, now)
	if err != nil {
		return st, err
	}
	store := launchnotice.Store{DB: j.DB}
	for _, p := range ps {
		if j.Skip[p.UserID] {
			continue
		}
		st.Considered++
		params := PurchaseParams{Interval: p.Interval, Amount: Amount(p.Amount, p.Currency), Price: PriceLabel(p.Amount, p.Currency, p.Interval), Date: p.PeriodEnd.UTC().Format("2006-01-02"), Trialing: p.Trialing}
		j.send(ctx, store, &st, KindPurchase, p.UserID, p.Email, PurchaseKind(p.Subscription), params, now)
	}
	fs, err := j.failures(ctx, now)
	if err != nil {
		return st, err
	}
	for _, f := range fs {
		if j.Skip[f.UserID] {
			continue
		}
		st.Considered++
		params := FailureParams{GraceEnds: f.PeriodEnd.Add(entitlements.Grace).UTC().Format("2006-01-02")}
		j.send(ctx, store, &st, KindPaymentFailed, f.UserID, f.Email, FailureKind(f.PeriodEnd), params, now)
	}
	return st, nil
}

func (j *Job) send(ctx context.Context, store launchnotice.Store, st *Stats, metricKind string, uid uuid.UUID, to, kind string, params any, now time.Time) {
	// The in-app side of an account notice is not wanted here (the Plan card shows these states), so it is created already expired.
	if _, err := store.EnsureAny(ctx, uid, kind, params, now); err != nil {
		st.Failed++
		j.observe(metricKind, launchnotice.OutcomeFailed)
		return
	}
	raw, claimed, err := store.ClaimEmailAny(ctx, uid, kind)
	if err != nil {
		st.Failed++
		j.observe(metricKind, launchnotice.OutcomeFailed)
		return
	}
	if !claimed {
		st.Skipped++
		j.observe(metricKind, launchnotice.OutcomeSkipped)
		return
	}
	subject, text, ok := render(metricKind, raw)
	if !ok {
		_ = store.ReleaseClaim(ctx, uid, kind)
		st.Failed++
		j.observe(metricKind, launchnotice.OutcomeFailed)
		return
	}
	if err := j.Mailer.Send(ctx, email.Message{To: to, Subject: subject, Text: text, IdempotencyKey: "billing-" + kind + "-" + uid.String()}); err != nil {
		_ = store.ReleaseClaim(ctx, uid, kind)
		st.Failed++
		j.observe(metricKind, launchnotice.OutcomeFailed)
		j.log().Warn("billing email: send failed; will retry", "kind", metricKind, "user_id", uid.String())
		return
	}
	if err := store.MarkSent(ctx, uid, kind); err != nil {
		j.log().Warn("billing email: sent but not marked", "kind", metricKind, "user_id", uid.String(), "err", err.Error())
	}
	st.Sent++
	j.observe(metricKind, launchnotice.OutcomeSent)
}

// eligibleUser is the shared recipient rule: a verified, non-blank address on an account that is not disabled.
const eligibleUser = `u.email_verified AND COALESCE(btrim(u.email), '') <> '' AND NOT u.disabled`

func (j *Job) purchases(ctx context.Context, now time.Time) ([]Purchase, error) {
	rows, err := j.DB.QueryContext(ctx, `
		SELECT e.user_id, u.email, e.external_id, e.current_period_end, s.price_interval, s.unit_amount, COALESCE(s.currency, 'usd'), s.trialing
		  FROM entitlements e
		  JOIN billing_stripe_subs s ON s.subscription_id = e.external_id AND s.user_id = e.user_id
		  JOIN users u ON u.id = e.user_id
		 WHERE e.source = 'stripe' AND e.status = 'active' AND e.current_period_end IS NOT NULL
		   AND s.price_interval IS NOT NULL AND s.unit_amount IS NOT NULL
		   AND s.updated_at > $1 AND `+eligibleUser+`
		   AND NOT EXISTS (SELECT 1 FROM account_notices n WHERE n.user_id = e.user_id AND n.kind = 'billing_purchase_' || e.external_id AND n.email_sent_at IS NOT NULL)`,
		now.Add(-RecentFor))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Purchase
	for rows.Next() {
		var p Purchase
		if err := rows.Scan(&p.UserID, &p.Email, &p.Subscription, &p.PeriodEnd, &p.Interval, &p.Amount, &p.Currency, &p.Trialing); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (j *Job) failures(ctx context.Context, now time.Time) ([]Failure, error) {
	rows, err := j.DB.QueryContext(ctx, `
		SELECT e.user_id, u.email, e.current_period_end
		  FROM entitlements e
		  JOIN users u ON u.id = e.user_id
		 WHERE e.source = 'stripe' AND e.status = 'past_due' AND e.current_period_end IS NOT NULL
		   AND e.current_period_end + make_interval(secs => $1) > $2 AND `+eligibleUser+`
		   AND NOT EXISTS (SELECT 1 FROM account_notices n WHERE n.user_id = e.user_id
		                    AND n.kind = 'billing_failed_' || to_char(e.current_period_end AT TIME ZONE 'UTC', 'YYYYMMDD') AND n.email_sent_at IS NOT NULL)`,
		entitlements.Grace.Seconds(), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Failure
	for rows.Next() {
		var f Failure
		if err := rows.Scan(&f.UserID, &f.Email, &f.PeriodEnd); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// RenewalSource lists the annual Stripe subscriptions about to renew, for launchnotice.RenewalJob (the yearly-renewal reminder, at least 30
// days ahead). A subscription set to cancel does not renew and is not listed.
type RenewalSource struct{ DB *sql.DB }

// AnnualRenewals implements launchnotice.RenewalSource.
func (s RenewalSource) AnnualRenewals(ctx context.Context, from, to time.Time) ([]launchnotice.Renewal, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT e.user_id, COALESCE(u.email, ''), u.email_verified, e.current_period_end, sub.unit_amount, COALESCE(sub.currency, 'usd')
		  FROM entitlements e
		  JOIN billing_stripe_subs sub ON sub.subscription_id = e.external_id AND sub.user_id = e.user_id
		  JOIN users u ON u.id = e.user_id
		 WHERE e.source = 'stripe' AND e.status = 'active' AND NOT e.cancel_at_period_end
		   AND sub.price_interval = 'year' AND sub.unit_amount IS NOT NULL AND NOT u.disabled
		   AND e.current_period_end BETWEEN $1 AND $2`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []launchnotice.Renewal
	for rows.Next() {
		var r launchnotice.Renewal
		var amount int64
		var currency string
		if err := rows.Scan(&r.UserID, &r.Email, &r.Verified, &r.RenewsOn, &amount, &currency); err != nil {
			return nil, err
		}
		r.PriceLabel = PriceLabel(amount, currency, "year")
		out = append(out, r)
	}
	return out, rows.Err()
}
