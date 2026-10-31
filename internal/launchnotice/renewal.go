package launchnotice

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
)

// The yearly-renewal reminder: one email, at least 30 days before an annual Stripe subscription renews, stating the price, the renewal
// date and a link to cancel that opens the billing portal after sign-in. It is a service email (it goes out with engagement emails off) and there is one per renewal period:
// the notice kind carries the renewal date, so next year's reminder is a new row. Apple subscriptions are not in the source: Apple sends
// its own reminders.
//
// THE COPY BELOW IS A DRAFT. DA reviews the real text before the billing item (Pro 7) enables this job; until then no source is wired
// and nothing is sent.

// RenewalKind is the notice kind of the reminder for the renewal on this date.
func RenewalKind(renewsOn time.Time) string { return "renewal_" + renewsOn.UTC().Format("20060102") }

// Renewal is one annual subscription about to renew.
type Renewal struct {
	UserID     uuid.UUID
	Email      string
	Verified   bool
	RenewsOn   time.Time
	PriceLabel string // "$34.99 a year"
}

// PlanURL is where the reminder sends the account to cancel: the Plan card in Settings, which opens the billing portal after sign-in. A
// fixed link on purpose: a pre-made billing-portal session URL expires and works as a bearer link to the customer's billing page if the
// email is forwarded, so the source cannot supply one.
const PlanURL = "https://rivolt.dev/settings#plan"

// RenewalSource lists the annual subscriptions renewing in [from, to].
type RenewalSource interface {
	AnnualRenewals(ctx context.Context, from, to time.Time) ([]Renewal, error)
}

// RenewalParams is what a reminder states.
type RenewalParams struct {
	RenewsOn   string `json:"renews_on"`
	PriceLabel string `json:"price"`
}

// RenewalSubject is the subject of the reminder.
func RenewalSubject(p RenewalParams) string {
	return "Your Rivolt Pro subscription renews on " + long(p.RenewsOn)
}

// RenderRenewalEmail is the draft reminder text.
func RenderRenewalEmail(p RenewalParams) string {
	return "Hi,\n\nYour Rivolt Pro subscription renews automatically on " + long(p.RenewsOn) + " at " + p.PriceLabel + ".\n\n" +
		"To cancel, in one step (sign in first): " + PlanURL + "\nCancelling keeps Pro until the end of the period you have paid for; nothing is deleted when a plan ends, and the full export is always free.\n\n" +
		"Nothing to do if you want to keep Pro.\n\nTerms: https://rivolt.dev/terms\n\nThe Rivolt team\nsupport@rivolt.dev\nRivolt is unofficial and not affiliated with Rivian.\n"
}

// RenewalJob sends the reminders.
type RenewalJob struct {
	Source RenewalSource
	Store  Store
	Mailer Mailer
	Now    func() time.Time
	Logger *slog.Logger
	Skip   map[uuid.UUID]bool
}

// RunOnce reminds every account whose annual subscription renews within 45 days (and has not been reminded for that renewal). A renewal
// already inside 30 days is reminded all the same and counted late, stating its true date.
func (j *RenewalJob) RunOnce(ctx context.Context) (DatedStats, error) {
	var st DatedStats
	if j.Source == nil {
		return st, nil
	}
	now := time.Now()
	if j.Now != nil {
		now = j.Now()
	}
	log := j.Logger
	if log == nil {
		log = slog.Default()
	}
	rs, err := j.Source.AnnualRenewals(ctx, now, now.Add(SendFrom))
	if err != nil {
		return st, err
	}
	for _, r := range rs {
		if j.Skip[r.UserID] || !r.Verified || strings.TrimSpace(r.Email) == "" {
			continue
		}
		st.Considered++
		p := RenewalParams{RenewsOn: r.RenewsOn.UTC().Format("2006-01-02"), PriceLabel: r.PriceLabel}
		kind := RenewalKind(r.RenewsOn)
		if _, err := j.Store.EnsureAny(ctx, r.UserID, kind, p, r.RenewsOn.Add(24*time.Hour)); err != nil {
			st.Failed++
			continue
		}
		raw, claimed, err := j.Store.ClaimEmailAny(ctx, r.UserID, kind)
		if err != nil {
			st.Failed++
			continue
		}
		if !claimed {
			st.Skipped++
			continue
		}
		var sp RenewalParams
		if err := json.Unmarshal(raw, &sp); err != nil {
			_ = j.Store.ReleaseClaim(ctx, r.UserID, kind)
			st.Failed++
			continue
		}
		if r.RenewsOn.Sub(now) < MinNotice {
			st.Late++
			log.Warn("renewal reminder: late", "late", true, "user_id", r.UserID.String(), "shortfall_days", int((MinNotice-r.RenewsOn.Sub(now)).Hours()/24))
		}
		err = j.Mailer.Send(ctx, email.Message{To: strings.TrimSpace(r.Email), Subject: RenewalSubject(sp), Text: RenderRenewalEmail(sp),
			IdempotencyKey: "renewal-" + kind + "-" + r.UserID.String()})
		if err != nil {
			_ = j.Store.ReleaseClaim(ctx, r.UserID, kind)
			st.Failed++
			continue
		}
		if err := j.Store.MarkSent(ctx, r.UserID, kind); err != nil {
			return st, err
		}
		st.Sent++
	}
	return st, nil
}
