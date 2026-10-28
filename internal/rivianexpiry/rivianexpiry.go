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
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/statement"
)

// Kind is the engagement_sends kind of the notices.
const Kind = "rivian_expiry"

// UnsubPurpose scopes a signed one-click link to these reminders: the statement email's
// unsubscribe accepts only YYYY-MM and the welcome and setup emails' only "onboarding".
const UnsubPurpose = "rivian-expiry"

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
	// OptedOut: the user stopped these reminders (Settings, or the link in the email).
	OptedOut bool
	// SignedIn is when the stored Rivian session was made (users.rivian_signed_in_at); zero when
	// unknown. The sweep never opens the session itself.
	SignedIn time.Time
}

// ExpiresAt is when the session is expected to stop working: the sign-in plus lifetime; zero when the
// sign-in time is unknown.
func (e Entry) ExpiresAt(lifetime time.Duration) time.Time {
	if e.SignedIn.IsZero() {
		return time.Time{}
	}
	return e.SignedIn.Add(lifetime)
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
	// Signer signs the one-click "stop these reminders" link. Nil, or a nil result: nothing is
	// sent (no way to opt out, no mail).
	Signer func() *statement.Signer
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
		exp := e.ExpiresAt(j.lifetime())
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
		j.send(ctx, e, days, exp, now)
	}
	if j.OnCounts != nil {
		j.OnCounts(c)
	}
}

func (j *Job) send(ctx context.Context, e Entry, days int, expires time.Time, now time.Time) {
	if j.Mail == nil {
		j.notice(days, "no_mailer") // counting only (no mailer, or emails switched off)
		return
	}
	if e.OptedOut {
		j.notice(days, "opted_out")
		return
	}
	if j.Signer == nil || j.Signer() == nil {
		j.notice(days, "no_signer") // no way to opt out yet: send nothing
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
	key := fmt.Sprintf("%d:%d", e.SignedIn.Unix(), days)
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
	if err := j.Mail.Send(ctx, buildMessage(to, expires, j.BaseURL, j.Signer().Sign(e.User, UnsubPurpose, now.Add(statement.TokenTTL)))); err != nil {
		status = "failed"
		j.log().Warn("rivian expiry notice: send", "days", days, "err", err.Error())
	}
	if err := j.Claims.Mark(ctx, e.User, Kind, key, status, j.now()); err != nil {
		j.log().Warn("rivian expiry notice: mark", "err", err.Error())
	}
	j.notice(days, status)
}

// buildMessage is the reminder (the owner's N49 wording): the date the connection ends, why,
// what stops, where to reconnect, and a one-click link to stop these reminders.
func buildMessage(to string, expires time.Time, baseURL, token string) email.Message {
	base := strings.TrimRight(baseURL, "/")
	where := "Settings → Rivian account → Sign in"
	unsub := base + "/api/rivian-expiry-email/unsubscribe?t=" + token
	text := "Your Rivian connection in Rivolt expires on " + expires.UTC().Format("January 2, 2006") + ".\n\n" +
		"Rivian asks for a fresh sign-in every 180 days. After that date Rivolt stops recording drives and charges until you reconnect.\n\n" +
		"Reconnect now: " + where + ". It takes a minute and keeps your history.\n"
	if base != "" {
		text += "\n  " + base + "/settings#rivian\n"
	}
	text += "\nStop these reminders: " + unsub + "\n"
	return email.Message{
		To:      to,
		Subject: "Reconnect Rivian to keep recording",
		Text:    text,
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + unsub + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}
}
