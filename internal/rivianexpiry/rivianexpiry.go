// Package rivianexpiry warns about a Rivian session before it expires.
//
// Rivian ends a user session 180 days after sign-in and gives the app no way to extend it
// (see rivian.SessionLifetime), so a user who never signs in again loses recording on that
// day, silently. The sweep finds sessions with at most 14 days left, counts them for the
// metric, and emails each user once at 7 days and once at 1 day left. It is read-only on
// the sessions; the notices are claimed in engagement_sends so each goes out at most once
// across replicas and restarts, and a new sign-in (a new session time) starts over.
package rivianexpiry

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/rivian"
)

// Kind is the engagement_sends kind of the notices.
const Kind = "rivian_expiry"

// Horizons, in days left.
const (
	WarnDays  = 7
	FinalDays = 1
	CountDays = 14
)

// Entry is a user with a stored Rivian session.
type Entry struct {
	User        uuid.UUID
	NeedsReauth bool
	Session     rivian.Session
}

// Sessions lists every stored Rivian session.
type Sessions interface {
	List(ctx context.Context) ([]Entry, error)
}

// Mailer sends an email.
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// Counts is what a sweep saw.
type Counts struct {
	Within14, Within7, Within1, Expired int
}

// Job is the sweep. Set the fields; Lifetime defaults to rivian.SessionLifetime.
type Job struct {
	Sessions Sessions
	Claims   engage.Store
	Mail     Mailer
	// Email returns the address on file ("" when none).
	Email   func(ctx context.Context, uid uuid.UUID) (string, error)
	BaseURL string
	// Skip excludes a user (the demo account).
	Skip     func(uid uuid.UUID) bool
	Lifetime time.Duration
	Now      func() time.Time
	Log      *slog.Logger
	// OnCounts gets the counts of each sweep; OnNotice each notice outcome ("sent",
	// "failed", "no_email", "already_sent") by days (WarnDays or FinalDays).
	OnCounts func(Counts)
	OnNotice func(days int, outcome string)
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) lifetime() time.Duration {
	if j.Lifetime > 0 {
		return j.Lifetime
	}
	return rivian.SessionLifetime
}

func (j *Job) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

func (j *Job) notice(days int, outcome string) {
	if j.OnNotice != nil {
		j.OnNotice(days, outcome)
	}
}

// Sweep counts the sessions near expiry and sends the notices due. A user whose session is
// already flagged needs_reauth is left to that flow.
func (j *Job) Sweep(ctx context.Context) {
	entries, err := j.Sessions.List(ctx)
	if err != nil {
		j.log().Warn("rivian expiry sweep: list sessions", "err", err.Error())
		return
	}
	now := j.now()
	var c Counts
	for _, e := range entries {
		if e.NeedsReauth || (j.Skip != nil && j.Skip(e.User)) {
			continue
		}
		exp := e.Session.ExpiresAt(j.lifetime())
		if exp.IsZero() {
			continue
		}
		left := exp.Sub(now)
		switch {
		case left <= 0:
			c.Expired++
			continue
		case left <= time.Duration(FinalDays)*24*time.Hour:
			c.Within1++
			c.Within7++
			c.Within14++
		case left <= time.Duration(WarnDays)*24*time.Hour:
			c.Within7++
			c.Within14++
		case left <= time.Duration(CountDays)*24*time.Hour:
			c.Within14++
			continue
		default:
			continue
		}
		days := WarnDays
		if left <= time.Duration(FinalDays)*24*time.Hour {
			days = FinalDays
		}
		j.send(ctx, e, days, left, now)
	}
	if j.OnCounts != nil {
		j.OnCounts(c)
	}
}

func (j *Job) send(ctx context.Context, e Entry, days int, left time.Duration, now time.Time) {
	if j.Mail == nil {
		j.notice(days, "no_mailer") // counting only (no mailer, or emails switched off)
		return
	}
	to, err := j.Email(ctx, e.User)
	if err != nil {
		j.log().Warn("rivian expiry notice: look up the address", "err", err.Error())
		return
	}
	if strings.TrimSpace(to) == "" {
		j.notice(days, "no_email") // nothing is claimed: an address added later still gets it
		return
	}
	key := fmt.Sprintf("%d:%d", e.Session.AuthenticatedAt.Unix(), days)
	msg := engage.Msg{User: e.User, Kind: Kind, Key: key, Channel: engage.Email, Utility: true}
	claimed, _, err := j.Claims.Claim(ctx, msg, now, now.Add(-engage.BudgetWindow), engage.MonthlyBudget)
	if err != nil {
		j.log().Warn("rivian expiry notice: claim", "err", err.Error())
		return
	}
	if !claimed {
		j.notice(days, "already_sent")
		return
	}
	status := "sent"
	if err := j.Mail.Send(ctx, buildMessage(to, left, j.BaseURL)); err != nil {
		status = "failed"
		j.log().Warn("rivian expiry notice: send", "days", days, "err", err.Error())
	}
	if err := j.Claims.Mark(ctx, e.User, Kind, key, status, j.now()); err != nil {
		j.log().Warn("rivian expiry notice: mark", "err", err.Error())
	}
	j.notice(days, status)
}

func buildMessage(to string, left time.Duration, baseURL string) email.Message {
	n := int(math.Ceil(left.Hours() / 24))
	when := fmt.Sprintf("in about %d days", n)
	if n <= 1 {
		when = "within a day"
	}
	link := "your Rivolt settings (Settings, Rivian account)"
	if baseURL != "" {
		link = strings.TrimRight(baseURL, "/") + "/settings#rivian"
	}
	return email.Message{
		To:      to,
		Subject: "Your Rivian connection to Rivolt ends " + when,
		Text: "Rivian ends a signed-in session 180 days after you sign in, and it cannot be\n" +
			"extended, so Rivolt's connection to your Rivian account will stop " + when + ".\n" +
			"When it does, drives and charging sessions stop recording.\n\n" +
			"To keep recording without a gap, sign in to Rivian again here (it takes a\n" +
			"minute and starts a new 180 days):\n\n" +
			"  " + link + "\n\n" +
			"Your history is safe either way: if the connection lapses, signing in\n" +
			"afterwards resumes recording from that point.\n",
	}
}
