package launchnotice

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
)

// The dated notices (Rivolt Pro, Sprint 14, item 11): the founding-year-end notice, at least 30 days before each founding row ends, and the
// yearly-renewal reminder, at least 30 days before an annual subscription renews. Both are the same machinery as the launch notice: a row in
// account_notices per (account, kind), an in-app notice, and a service email that is claimed, sent and marked, ignoring engagement settings.

// KindFoundingEnd is the founding-year-end notice.
const KindFoundingEnd = "founding_end"

// Notice windows. The promise (Terms section 5 and the launch email) is "at least 30 days before that date"; the job starts sending at
// 45 days so an outage of a few days cannot eat the margin.
const (
	MinNotice = 30 * 24 * time.Hour
	SendFrom  = 45 * 24 * time.Hour
)

// The transactional billing emails' metric kinds (internal/billingmail): the purchase confirmation and the payment-failed notice.
const (
	KindBillingPurchase      = "billing_purchase"
	KindBillingPaymentFailed = "billing_payment_failed"
)

// KindAnnualRenewal is the metric kind of the yearly-renewal reminder (its notice rows are per period: see RenewalKind).
const KindAnnualRenewal = "annual_renewal"

// The outcomes the dated jobs report (rivolt_dated_notices_total{kind,outcome}). Observe is only ever called with one of these; the
// metric pre-creates every (kind, outcome) pair at 0 so the first late notice is an increase, not a series that appears at 1.
const (
	OutcomeSent    = "sent"
	OutcomeInApp   = "in_app"
	OutcomeSkipped = "skipped"
	OutcomeFailed  = "failed"
	OutcomeLate    = "late"
)

// Outcomes lists them; MetricKinds lists the kinds. Add a new outcome here and the metric and its test follow.
var (
	Outcomes    = []string{OutcomeSent, OutcomeInApp, OutcomeSkipped, OutcomeFailed, OutcomeLate}
	MetricKinds = []string{KindFoundingEnd, KindAnnualRenewal, KindBillingPurchase, KindBillingPaymentFailed}
)

// FoundingParams are the dates a founding-year-end notice states.
type FoundingParams struct {
	End string `json:"end"` // the founding year's last day, YYYY-MM-DD
	// ExtendedFrom, when set, is the end date before the account's year was extended to give 30 days' notice.
	ExtendedFrom string `json:"extended_from,omitempty"`
	// Subscribed: a paid subscription outlasts the founding year, so nothing changes for the account on that date.
	Subscribed bool `json:"subscribed,omitempty"`
}

// FoundingShowsUntil is the end of the day the founding year ends.
func (p FoundingParams) FoundingShowsUntil() (time.Time, error) {
	t, err := time.Parse("2006-01-02", p.End)
	if err != nil {
		return time.Time{}, err
	}
	return t.Add(24 * time.Hour), nil
}

// FoundingSubject is the subject of the founding-year-end email.
func FoundingSubject(p FoundingParams) string {
	return "Your free year of Rivolt Pro ends on " + long(p.End)
}

// RenderFoundingEmail is the plain-text founding-year-end email (copy section 4). A subscribed account gets the line that nothing changes
// for it on that date; an extended year says so.
func RenderFoundingEmail(p FoundingParams) string {
	end := long(p.End)
	var b strings.Builder
	b.WriteString("Hi,\n\nYour account has had Rivolt Pro at no charge since Pro launched. That ends on " + end + ". Nothing is charged and nothing is deleted.\n\n")
	if p.ExtendedFrom != "" {
		b.WriteString("We moved this date from " + long(p.ExtendedFrom) + " to " + end + " so that you have at least 30 days' notice.\n\n")
	}
	if p.Subscribed {
		b.WriteString("Your Rivolt Pro subscription continues; nothing changes for you on " + end + ".\n\n")
	}
	b.WriteString(foundingBody)
	return b.String()
}

// RenderFoundingInApp is the in-app notice for the founding-year end.
func RenderFoundingInApp(p FoundingParams) InApp {
	end := long(p.End)
	body := "Your free year of Rivolt Pro ends on " + end + ". Nothing is charged and nothing is deleted; the full export stays free."
	if p.Subscribed {
		body = "Your Rivolt Pro subscription continues; nothing changes for you on " + end + "."
	}
	if p.ExtendedFrom != "" {
		body += " We moved the date from " + long(p.ExtendedFrom) + " to give you at least 30 days' notice."
	}
	return InApp{
		Title: "Your free year of Rivolt Pro ends on " + end,
		Body:  body,
		Links: []Link{{Label: "What changes", Href: "/support#free-and-pro"}, {Label: "Plan", Href: "/settings"}},
	}
}

const foundingBody = `What changes on that date: Activity, Money and Truck show the last 90 days;
the planner plans single-day trips; three saved trips stay editable and the rest
stay read-only; one vehicle is shown at a time; trends and range near a
temperature are not shown; the iPhone widgets and the charging Live Activity
show the free state.

What does not change: every drive and charge keeps being recorded on every
connected vehicle, every alert keeps working, and the full export of everything,
including history older than 90 days, stays free. Export it first if you like:
Settings -> Data.

To keep Pro: $3.99 a month or $34.99 a year, in Settings -> Account -> Plan or in the
iPhone app. Or do nothing.

The Rivolt team
support@rivolt.dev
Rivolt is unofficial and not affiliated with Rivian.
`

// Mailer sends one message.
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// FoundingCandidate is one account with a running founding year.
type FoundingCandidate struct {
	UserID     uuid.UUID
	Email      string
	Verified   bool
	End        time.Time // the founding row's current_period_end
	Subscribed bool      // a paid subscription that outlasts the founding year
}

// Prepared is what a founding-year-end notice states, fixed in the same transaction that claimed it.
type Prepared struct {
	Params FoundingParams
	// Claimed: the account's email is ours to send (false for an account with no email to send, or one another run holds).
	Claimed bool
	// Extended: the year was extended to give 30 days' notice; Shortfall is how many days short it would have been.
	Extended  bool
	Shortfall time.Duration
}

// DatedStore is what the job needs from the database, so tests run on a fake.
type DatedStore interface {
	// FoundingCandidates lists accounts whose founding year ends within SendFrom and that still need a notice (or, for a verified address, an email).
	FoundingCandidates(ctx context.Context, now time.Time) ([]FoundingCandidate, error)
	// PrepareFounding, in one transaction, extends a year that is under 30 days from ending to now + 30 days, fixes the notice's dates in
	// account_notices and (when wantEmail) claims the email. Nothing is changed when the email is wanted but cannot be claimed.
	PrepareFounding(ctx context.Context, c FoundingCandidate, now time.Time, wantEmail bool) (Prepared, error)
	MarkSent(ctx context.Context, uid uuid.UUID, kind string) error
	ReleaseClaim(ctx context.Context, uid uuid.UUID, kind string) error
}

// Outcomes of one run, counts only.
type DatedStats struct {
	Considered, InApp, Sent, Failed, Skipped int
	Late                                     int           // years that had to be extended
	WorstShortfall                           time.Duration // the largest shortfall among them
}

// DatedJob sends the founding-year-end notices.
type DatedJob struct {
	Store  DatedStore
	Mailer Mailer
	Now    func() time.Time
	Logger *slog.Logger
	// Skip excludes accounts (the demo and auth-bypass users).
	Skip map[uuid.UUID]bool
	// OnExtend is told after an account's founding year was extended (invalidate the plan cache).
	OnExtend func(uid uuid.UUID)
	// Observe counts an outcome ("sent", "failed", "in_app", "skipped", "late").
	Observe func(kind, outcome string)
}

func (j *DatedJob) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *DatedJob) observe(kind, outcome string) {
	if j.Observe != nil {
		j.Observe(kind, outcome)
	}
}

func (j *DatedJob) log() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// RunOnce gives every account whose founding year ends within 45 days its notice: in-app for all, the email too for a verified address.
// It is safe on every replica (the claim is a database row) and idempotent (an account is noticed once, and an email retried only after a
// failure). A year under 30 days from its end is extended to 30 days from now first, so the promise is kept, and counted as late.
func (j *DatedJob) RunOnce(ctx context.Context) (DatedStats, error) {
	var st DatedStats
	now := j.now()
	cands, err := j.Store.FoundingCandidates(ctx, now)
	if err != nil {
		return st, err
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		if j.Skip[c.UserID] {
			continue
		}
		st.Considered++
		wantEmail := c.Verified && strings.TrimSpace(c.Email) != ""
		prep, err := j.Store.PrepareFounding(ctx, c, now, wantEmail)
		if err != nil {
			st.Failed++
			j.observe(KindFoundingEnd, OutcomeFailed)
			j.log().Warn("founding notice: not prepared", "user_id", c.UserID.String(), "err", errString(err))
			continue
		}
		if prep.Extended {
			st.Late++
			if prep.Shortfall > st.WorstShortfall {
				st.WorstShortfall = prep.Shortfall
			}
			j.observe(KindFoundingEnd, OutcomeLate)
			j.log().Warn("founding notice: late; the year was extended to keep 30 days' notice", "late", true,
				"user_id", c.UserID.String(), "shortfall_days", int(prep.Shortfall.Hours()/24), "new_end", prep.Params.End)
			if j.OnExtend != nil {
				j.OnExtend(c.UserID)
			}
		}
		if !wantEmail {
			st.InApp++
			j.observe(KindFoundingEnd, OutcomeInApp)
			continue
		}
		if !prep.Claimed {
			st.Skipped++
			j.observe(KindFoundingEnd, OutcomeSkipped)
			continue
		}
		err = j.Mailer.Send(ctx, email.Message{
			To: strings.TrimSpace(c.Email), Subject: FoundingSubject(prep.Params), Text: RenderFoundingEmail(prep.Params),
			IdempotencyKey: "founding-end-" + c.UserID.String(),
		})
		if err != nil {
			_ = j.Store.ReleaseClaim(ctx, c.UserID, KindFoundingEnd)
			st.Failed++
			j.observe(KindFoundingEnd, OutcomeFailed)
			j.log().Warn("founding notice: send failed", "user_id", c.UserID.String(), "err", errString(err))
			continue
		}
		if err := j.Store.MarkSent(ctx, c.UserID, KindFoundingEnd); err != nil {
			st.Failed++
			j.log().Error("founding notice: sent but not recorded", "user_id", c.UserID.String(), "err", errString(err))
			return st, err
		}
		st.Sent++
		st.InApp++
		j.observe(KindFoundingEnd, OutcomeSent)
	}
	return st, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprint(err.Error())
}

// ---- Postgres

// PGDated is DatedStore over the system pool.
type PGDated struct{ DB *sql.DB }

func (s PGDated) FoundingCandidates(ctx context.Context, now time.Time) ([]FoundingCandidate, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT e.user_id, COALESCE(u.email, ''), u.email_verified, e.current_period_end,
		       EXISTS (SELECT 1 FROM entitlements p
		                WHERE p.user_id = e.user_id AND p.source IN ('stripe', 'apple') AND p.status IN ('active', 'past_due')
		                  AND (p.current_period_end IS NULL OR p.current_period_end > e.current_period_end))
		  FROM entitlements e
		  JOIN users u ON u.id = e.user_id
		  LEFT JOIN account_notices n ON n.user_id = e.user_id AND n.kind = $3
		 WHERE e.source = 'founding' AND e.status = 'active' AND NOT u.disabled
		   AND e.current_period_end > $1 AND e.current_period_end <= $2
		   AND (n.user_id IS NULL OR (n.email_sent_at IS NULL AND u.email_verified AND COALESCE(u.email, '') <> ''))
		 ORDER BY e.current_period_end, e.user_id`, now, now.Add(SendFrom), KindFoundingEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FoundingCandidate
	for rows.Next() {
		var c FoundingCandidate
		if err := rows.Scan(&c.UserID, &c.Email, &c.Verified, &c.End, &c.Subscribed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s PGDated) PrepareFounding(ctx context.Context, c FoundingCandidate, now time.Time, wantEmail bool) (Prepared, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Prepared{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var end time.Time
	err = tx.QueryRowContext(ctx, `SELECT current_period_end FROM entitlements WHERE user_id = $1 AND source = 'founding' AND status = 'active' FOR UPDATE`, c.UserID).Scan(&end)
	if errors.Is(err, sql.ErrNoRows) {
		return Prepared{}, nil
	}
	if err != nil {
		return Prepared{}, err
	}
	if !end.After(now) {
		return Prepared{}, nil // the year is over; there is nothing to give notice of
	}
	// The dates already fixed for this account (an earlier run created the in-app notice).
	var old FoundingParams
	var raw []byte
	switch err := tx.QueryRowContext(ctx, `SELECT params FROM account_notices WHERE user_id = $1 AND kind = $2`, c.UserID, KindFoundingEnd).Scan(&raw); {
	case err == nil:
		_ = json.Unmarshal(raw, &old)
	case !errors.Is(err, sql.ErrNoRows):
		return Prepared{}, err
	}
	p := FoundingParams{End: end.UTC().Format("2006-01-02"), ExtendedFrom: old.ExtendedFrom, Subscribed: c.Subscribed}
	var prep Prepared
	if end.Sub(now) < MinNotice {
		newEnd := now.Add(MinNotice)
		prep.Extended, prep.Shortfall = true, MinNotice-end.Sub(now)
		if p.ExtendedFrom == "" {
			p.ExtendedFrom = end.UTC().Format("2006-01-02")
		}
		p.End = newEnd.UTC().Format("2006-01-02")
		if _, err := tx.ExecContext(ctx, `UPDATE entitlements SET current_period_end = $2, updated_at = now() WHERE user_id = $1 AND source = 'founding'`, c.UserID, newEnd); err != nil {
			return Prepared{}, err
		}
	}
	until, err := p.FoundingShowsUntil()
	if err != nil {
		return Prepared{}, err
	}
	rawP, _ := json.Marshal(p)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_notices (user_id, kind, params, shows_until) VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, kind) DO UPDATE SET params = EXCLUDED.params, shows_until = EXCLUDED.shows_until
		WHERE account_notices.email_sent_at IS NULL`, c.UserID, KindFoundingEnd, rawP, until); err != nil {
		return Prepared{}, err
	}
	prep.Params = p
	if wantEmail {
		var one int
		err := tx.QueryRowContext(ctx, `
			UPDATE account_notices SET email_claimed_at = now()
			 WHERE user_id = $1 AND kind = $2 AND email_sent_at IS NULL
			   AND (email_claimed_at IS NULL OR email_claimed_at < now() - make_interval(secs => $3))
			RETURNING 1`, c.UserID, KindFoundingEnd, ClaimStale.Seconds()).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			// Another run holds the email: change nothing (an extension without a notice would be a silent change).
			return Prepared{}, nil
		}
		if err != nil {
			return Prepared{}, err
		}
		prep.Claimed = true
	}
	if err := tx.Commit(); err != nil {
		return Prepared{}, err
	}
	return prep, nil
}

func (s PGDated) MarkSent(ctx context.Context, uid uuid.UUID, kind string) error {
	return Store(s).MarkSent(ctx, uid, kind)
}

func (s PGDated) ReleaseClaim(ctx context.Context, uid uuid.UUID, kind string) error {
	return Store(s).ReleaseClaim(ctx, uid, kind)
}
