package statement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/iosapp"
	"github.com/apohor/rivolt/internal/ledger"
)

// Mailer is the part of the email client the job uses.
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// MonthlyFunc returns the ledger's own monthly statement (all
// vehicles) for the user and YYYY-MM period in loc: the same code path
// as GET /api/ledger/monthly.
type MonthlyFunc func(ctx context.Context, uid uuid.UUID, period string, loc *time.Location) (ledger.Monthly, error)

// SavingsFunc returns the user's off-peak saving estimate for the period (nil
// when it cannot be made). It is only called for a user who is being sent the
// statement; an error just leaves the line out.
type SavingsFunc func(ctx context.Context, uid uuid.UUID, period string, loc *time.Location) (*ledger.Savings, error)

// Job sends the monthly statement emails. It is safe to run on every
// replica: the (user, period) claim row is the idempotency key.
type Job struct {
	Store   Store
	Mailer  Mailer
	Monthly MonthlyFunc
	Savings SavingsFunc // optional
	Signer  *Signer
	// Enabled reports whether the operator flag covers the user.
	Enabled func(userID string) bool
	BaseURL string
	Logger  *slog.Logger
	// Now and Pause are overridable for tests. Pause is called between
	// sends (the throttle); nil uses a 500 ms sleep (2 sends/s).
	Now   func() time.Time
	Pause func(ctx context.Context)
}

// Stats is what one run did.
type Stats struct {
	Considered, Due, Sent, Failed, Skipped, AlreadyDone int
	// StuckSending is claims left 'sending' for over an hour.
	StuckSending int
}

const (
	sendFromHour = 8 // not before 08:00 local
	maxSendDay   = 2 // local day of month 1 or 2
)

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) pause(ctx context.Context) {
	if j.Pause != nil {
		j.Pause(ctx)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
	}
}

func (j *Job) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// Due reports whether a user in tz is due at now, and the period (the
// previous full local month) to send.
func Due(now time.Time, tz string) (period string, loc *time.Location, due bool) {
	loc, err := time.LoadLocation(strings.TrimSpace(tz))
	if err != nil || strings.EqualFold(tz, "local") {
		loc = time.UTC
	}
	l := now.In(loc)
	if l.Day() > maxSendDay || l.Hour() < sendFromHour {
		return "", loc, false
	}
	return PreviousMonth(now, loc), loc, true
}

// RunOnce considers every opted-in user once. Errors on one user never
// stop the others.
func (j *Job) RunOnce(ctx context.Context) (Stats, error) {
	var st Stats
	if _, err := j.Store.Purge(ctx, j.now().Add(-SendRetention)); err != nil {
		j.logger().Warn("statement email: purge", "err", err.Error())
	}
	if n, err := j.Store.StuckSending(ctx, time.Hour, j.now()); err != nil {
		j.logger().Warn("statement email: stuck check", "err", err.Error())
	} else if n > 0 {
		st.StuckSending = n
		j.logger().Warn("statement email: claims stuck in 'sending' (a send may have been lost; not retried to avoid duplicates)", "count", n)
	}
	cands, err := j.Store.OptedIn(ctx)
	if err != nil {
		return st, fmt.Errorf("statement email: list opted-in: %w", err)
	}
	first := true
	for _, c := range cands {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		st.Considered++
		// Both switches: the operator flag AND the user's opt-in
		// (OptedIn already filtered on it).
		if j.Enabled == nil || !j.Enabled(c.UserID.String()) {
			continue
		}
		period, loc, due := Due(j.now(), c.Timezone)
		if !due {
			continue
		}
		st.Due++
		claimed, err := j.Store.Claim(ctx, c.UserID, period, j.now())
		if err != nil {
			j.logger().Warn("statement email: claim", "period", period, "err", err.Error())
			st.Failed++
			continue
		}
		if !claimed {
			st.AlreadyDone++
			continue
		}
		if !first {
			j.pause(ctx)
		}
		first = false
		switch j.sendOne(ctx, c, period, loc) {
		case "sent":
			st.Sent++
		case "skipped":
			st.Skipped++
		default:
			st.Failed++
		}
	}
	return st, nil
}

func (j *Job) sendOne(ctx context.Context, c Candidate, period string, loc *time.Location) string {
	fail := func(reason string, err error) string {
		j.logger().Warn("statement email: not sent", "period", period, "reason", reason)
		_ = j.Store.Mark(ctx, c.UserID, period, "failed", reason, j.now())
		_ = err
		return "failed"
	}
	cur, err := j.Monthly(ctx, c.UserID, period, loc)
	if err != nil {
		return fail("ledger_unavailable", err) // never send partial or estimated totals
	}
	var prev *ledger.Monthly
	if t, perr := time.ParseInLocation("2006-01", period, loc); perr == nil {
		if p, perr := j.Monthly(ctx, c.UserID, t.AddDate(0, -1, 0).Format("2006-01"), loc); perr == nil {
			prev = &p
		}
	}
	var sv *ledger.Savings
	if j.Savings != nil {
		if s, serr := j.Savings(ctx, c.UserID, period, loc); serr == nil {
			sv = s
		}
	}
	msg, ok := j.BuildWith(c, period, cur, prev, sv)
	if !ok {
		_ = j.Store.Mark(ctx, c.UserID, period, "skipped", "silent_month", j.now())
		return "skipped"
	}
	if err := j.Mailer.Send(ctx, msg); err != nil {
		return fail("send_failed", err)
	}
	_ = j.Store.Mark(ctx, c.UserID, period, "sent", "", j.now())
	return "sent"
}

// Build renders the message for a candidate (also used by the preview
// endpoint so preview and send can never differ). ok is false for a
// silent month.
func (j *Job) Build(c Candidate, period string, cur ledger.Monthly, prev *ledger.Monthly) (email.Message, bool) {
	return j.BuildWith(c, period, cur, prev, nil)
}

// BuildWith is Build with the month's off-peak saving estimate (nil: no line).
func (j *Job) BuildWith(c Candidate, period string, cur ledger.Monthly, prev *ledger.Monthly, sv *ledger.Savings) (email.Message, bool) {
	r := Render(Input{
		Period: period, FirstName: firstName(c.FirstName), Currency: cur.Currency,
		Month: cur, Prev: prev, Links: j.linksFor(c, period), Savings: sv,
	})
	if r.Silent {
		return email.Message{}, false
	}
	unsub := j.unsubURL(c.UserID, period)
	return email.Message{
		To: c.Email, Subject: r.Subject, Text: r.Text, HTML: r.HTML,
		// A timeout after a successful send must not become a second
		// email on retry: Resend dedupes on this key for 24 h.
		IdempotencyKey: idempotencyKey(c.UserID, period),
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + unsub + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}, true
}

func (j *Job) base() string { return strings.TrimRight(j.BaseURL, "/") }

func (j *Job) unsubURL(uid uuid.UUID, period string) string {
	tok := j.Signer.Sign(uid, period, j.now().Add(TokenTTL))
	return j.base() + "/api/statement-email/unsubscribe?t=" + tok
}

// linksFor adds the iPhone app link, unless the account already signs in on the app.
func (j *Job) linksFor(c Candidate, period string) Links {
	l := j.links(c.UserID, period)
	if !c.HasApp {
		l.IOSApp = iosapp.URL()
	}
	return l
}

func (j *Job) links(uid uuid.UUID, period string) Links {
	b := j.base()
	return Links{
		Statement:   b + "/money?month=" + period + "&src=statement",
		Share:       b + "/money?month=" + period + "&src=statement&share=1",
		Unpriced:    b + "/charges?unpriced=1&src=statement",
		Settings:    b + "/settings?tab=notifications",
		Unsubscribe: j.unsubURL(uid, period),
	}
}

// firstName takes the first word of a display name; an email-like or
// empty name yields "".
func firstName(display string) string {
	f := strings.Fields(display)
	if len(f) == 0 || strings.Contains(f[0], "@") {
		return ""
	}
	return f[0]
}

// idempotencyKey is a stable, opaque per-(user, period) key; the raw
// user id never leaves the server.
func idempotencyKey(uid uuid.UUID, period string) string {
	sum := sha256.Sum256([]byte("rivolt/statement/" + uid.String() + "/" + period))
	return "stm-" + hex.EncodeToString(sum[:16])
}
