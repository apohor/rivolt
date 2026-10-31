// Package r2arrival sends the one "Your R2 may be here" email.
//
// A user who is waiting for an R2 may tell Rivolt the month they expect it (a month, never a day;
// engagement_prefs.r2_arrival_month, off until chosen). On the first weekday of that month, at 08:00 in their time zone, they
// get one email with the connect steps. It goes only to a verified address, only while no vehicle is connected, only when the
// user has not turned engagement emails off, counts against the engagement budget, and is claimed in engagement_sends under the
// month, so it is sent at most once across replicas and restarts. It is never sent after its month, and never a second time. Once
// its email is claimed (sent, failed or already sent) the month is deleted, and a month that has ended is deleted by the sweep: the
// due list is capped, so finished rows must not crowd later users out, and the month is not kept past its purpose.
package r2arrival

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/statement"
)

// Kind is the engagement_sends kind.
const Kind = "r2_arrival"

// UnsubPurpose scopes the signed one-click link in the email to this reminder alone.
const UnsubPurpose = "r2-arrival"

// SendHour is the local hour of the send. A user whose browser never stored a time zone gets the same message at FallbackUTCHour,
// the morning in the Americas, where the first R2s are.
const (
	SendHour        = 8
	FallbackUTCHour = 14
)

// Outcomes beyond Engage's own, as the job's callback names them.
const (
	OutcomeNoMailer = "no_mailer"
	OutcomeNoSigner = "no_signer"
)

// Source is the database side.
type Source interface {
	Due(ctx context.Context, from, to time.Time) ([]db.R2ArrivalDue, error)
	StillWanted(ctx context.Context, uid uuid.UUID, month time.Time) (bool, error)
	// Clear deletes the user's month if it is still this one; ClearEnded deletes every month that has ended.
	Clear(ctx context.Context, uid uuid.UUID, month time.Time) error
	ClearEnded(ctx context.Context, now time.Time) (int64, error)
}

// Mailer sends an email.
type Mailer interface {
	Send(ctx context.Context, m email.Message) error
}

// Job is the sweep. Set Source, Engage and Mail; Mail nil counts nothing and sends nothing.
type Job struct {
	Source Source
	Engage *engage.Engage
	Mail   Mailer
	// BaseURL is the public origin for the links in the email.
	BaseURL string
	// Signer signs the one-click link. Nil, or a nil result: nothing is sent (no way to opt out, no mail).
	Signer func() *statement.Signer
	// Skip excludes a user (the demo account).
	Skip func(uid uuid.UUID) bool
	Now  func() time.Time
	Log  *slog.Logger
	// OnOutcome is told each send attempt's outcome (engage's, or OutcomeNoSigner).
	OnOutcome func(outcome string)
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

func (j *Job) log() *slog.Logger {
	if j.Log != nil {
		return j.Log
	}
	return slog.Default()
}

// SendTime is the first weekday of month (its first day, UTC) at SendHour in loc.
func SendTime(month time.Time, loc *time.Location) time.Time {
	return firstWeekdayAt(month, loc, SendHour)
}

func firstWeekdayAt(month time.Time, loc *time.Location, hour int) time.Time {
	for day := 1; ; day++ {
		t := time.Date(month.Year(), month.Month(), day, hour, 0, 0, 0, loc)
		if wd := t.Weekday(); wd != time.Saturday && wd != time.Sunday {
			return t
		}
	}
}

// window is when the email for month may go: from its send time until the month ends in loc.
func window(month time.Time, tz string, known bool) (start, end time.Time) {
	loc := time.UTC
	hour := FallbackUTCHour
	if known {
		if l, err := time.LoadLocation(tz); err == nil {
			loc, hour = l, SendHour
		}
	}
	return firstWeekdayAt(month, loc, hour), time.Date(month.Year(), month.Month()+1, 1, 0, 0, 0, 0, loc)
}

// Sweep sends the emails due now. Safe on every replica: Engage claims each (user, month).
func (j *Job) Sweep(ctx context.Context) {
	if j.Mail == nil {
		return
	}
	now := j.now()
	if n, err := j.Source.ClearEnded(ctx, now); err != nil {
		j.log().Warn("r2 arrival sweep: clear ended months", "err", err.Error())
	} else if n > 0 {
		j.log().Info("r2 arrival: deleted ended months", "count", n)
	}
	// Candidate months: this one in any zone, so the month before and after UTC's.
	cur := time.Date(now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	due, err := j.Source.Due(ctx, cur.AddDate(0, -1, 0), cur.AddDate(0, 1, 0))
	if err != nil {
		j.log().Warn("r2 arrival sweep: list", "err", err.Error())
		return
	}
	for _, c := range due {
		if j.Skip != nil && j.Skip(c.UserID) {
			continue
		}
		tz, _, known, err := j.Engage.Store.Timezone(ctx, c.UserID)
		if err != nil {
			j.log().Warn("r2 arrival sweep: time zone", "err", err.Error())
			continue
		}
		start, end := window(c.Month, tz, known)
		if now.Before(start) || !now.Before(end) {
			continue // not yet, or its month has passed: never sent late
		}
		j.send(ctx, c, now)
	}
}

func (j *Job) send(ctx context.Context, c db.R2ArrivalDue, now time.Time) {
	if j.Signer == nil || j.Signer() == nil {
		j.outcome(OutcomeNoSigner)
		return
	}
	token := j.Signer().Sign(c.UserID, UnsubPurpose, now.Add(statement.TokenTTL))
	m := engage.Msg{
		User: c.UserID, Kind: Kind, Key: c.Month.Format("2006-01"), Channel: engage.Email,
		Toggle: func(ctx context.Context) (bool, error) { return j.Source.StillWanted(ctx, c.UserID, c.Month) },
	}
	outcome, err := j.Engage.Send(ctx, m, func(ctx context.Context) error {
		return j.Mail.Send(ctx, BuildMessage(c.Email, c.Month, j.BaseURL, token))
	})
	if err != nil {
		j.log().Warn("r2 arrival send", "outcome", outcome, "err", err.Error())
	}
	// The claim is final for sent, failed (never retried under the month) and already sent: the month has served its purpose.
	// A skip (budget, no longer wanted, fenced) is tried again, so the month stays.
	switch outcome {
	case engage.Sent, engage.Failed, engage.AlreadySent:
		if err := j.Source.Clear(ctx, c.UserID, c.Month); err != nil {
			j.log().Warn("r2 arrival: delete the month", "err", err.Error())
		}
	}
}

func (j *Job) outcome(o string) {
	if j.OnOutcome != nil {
		j.OnOutcome(o)
	}
}

// BuildMessage is the email (DA-approved wording, Oct 31): the connect steps for an R2, how to change or delete the reminder, and
// a one-click link that deletes it.
func BuildMessage(to string, month time.Time, baseURL, token string) email.Message {
	base := strings.TrimRight(baseURL, "/")
	unsub := base + "/api/r2-arrival/unsubscribe?t=" + token
	text := "Hi,\n\n" +
		"You asked for one note in " + month.Month().String() + ": when your R2 arrives, here is how to connect it to Rivolt.\n\n" +
		"1. In the Rivian app, add the second Rivian account you created as an Authorized Driver on the R2, and accept the invite by signing in to the Rivian app once with that account.\n" +
		"2. In Rivolt, Settings → Account → Connect Rivian, and sign in with that account. Your password is used once and never stored.\n" +
		"3. Drive or charge once; it shows up in Activity once it ends.\n\n" +
		"If the truck is still a way off, change the month in Settings; if you'd rather not hear from us, delete the reminder there. " +
		"Drives, charges and plug-in are recorded on the first R2s on Rivolt; a few fields aren't in yet, and the Truck tab says which.\n\n" +
		"The Rivolt team\nsupport@rivolt.dev\nRivolt is unofficial and not affiliated with Rivian.\n\n" +
		"Delete this reminder: " + unsub + "\n"
	return email.Message{
		To:      to,
		Subject: "Your R2 may be here: how to connect it to Rivolt",
		Text:    text,
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + unsub + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	}
}
