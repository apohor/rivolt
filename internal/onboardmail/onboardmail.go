// Package onboardmail sends the welcome email and the setup reminders: at
// most three emails per user ever (one welcome, two reminders), only to a
// verified address, only until the truck is connected and sending data, and
// never after the user opted out. Every email carries a signed one-click
// unsubscribe link and List-Unsubscribe headers. No tracking pixel, no
// rewritten links.
package onboardmail

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/iosapp"
	"github.com/apohor/rivolt/internal/statement"
)

// Timing of the setup reminders: the first once the account is three days
// old, the follow-up six days after the first.
const (
	NudgeMinAge   = 72 * time.Hour
	NudgeFollowUp = 6 * 24 * time.Hour
)

// UnsubPurpose is the token "period" that scopes a signed link to these
// emails: the statement email's unsubscribe only accepts YYYY-MM.
const UnsubPurpose = "onboarding"

// Sender is the part of the Resend client the job uses.
type Sender interface {
	Send(ctx context.Context, m email.Message) error
}

// Job sweeps for users due a welcome or a reminder and sends it.
type Job struct {
	DB   *sql.DB
	Mail Sender
	// Signer returns the unsubscribe-link signer, or nil: then nothing is
	// sent (no way to unsubscribe). A func because the signer is built after
	// the sweep is wired.
	Signer  func() *statement.Signer
	BaseURL string
	Logger  *slog.Logger
	Now     func() time.Time
	// OnSent, when set, is told after each send (metrics, funnel log).
	OnSent func(kind string, attempt int, uid uuid.UUID)
}

// Sweep runs one pass and returns how many welcomes and reminders it sent.
func (j *Job) Sweep(ctx context.Context) (welcomes, nudges int) {
	if j.Mail == nil || j.Signer == nil || j.Signer() == nil || j.DB == nil {
		return 0, 0
	}
	log := j.Logger
	if log == nil {
		log = slog.Default()
	}
	ws, err := db.ListUsersDueForWelcome(ctx, j.DB)
	if err != nil {
		log.Warn("welcome sweep: list failed", "err", err.Error())
	}
	for _, w := range ws {
		if ok, err := db.ClaimWelcome(ctx, j.DB, w.UserID); err != nil {
			log.Warn("welcome sweep: claim failed", "user_id", w.UserID.String(), "err", err.Error())
			continue
		} else if !ok {
			continue // another replica took it, or the user connected or opted out
		}
		if j.send(ctx, log, w.UserID, w.Email, "welcome", Welcome(j.welcomeLinks(w))) {
			welcomes++
		}
	}
	ns, err := db.ListUsersDueForSetupNudge(ctx, j.DB, NudgeMinAge, NudgeFollowUp)
	if err != nil {
		log.Warn("setup nudge sweep: list failed", "err", err.Error())
	}
	for _, n := range ns {
		if ok, err := db.ClaimSetupNudge(ctx, j.DB, n.UserID, n.Attempt); err != nil {
			log.Warn("setup nudge sweep: claim failed", "user_id", n.UserID.String(), "err", err.Error())
			continue
		} else if !ok {
			continue
		}
		if j.send(ctx, log, n.UserID, n.Email, "setup_nudge", Nudge(n.Kind, n.Attempt, j.links(n.UserID))) {
			nudges++
		}
	}
	return welcomes, nudges
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now()
	}
	return time.Now()
}

// welcomeLinks adds the iPhone app link to the welcome only, and not for an account that
// already signs in on the app.
func (j *Job) welcomeLinks(w db.Welcome) Links {
	l := j.links(w.UserID)
	if !w.HasApp {
		l.IOSApp = iosapp.URL()
	}
	return l
}

func (j *Job) links(uid uuid.UUID) Links {
	base := strings.TrimRight(j.BaseURL, "/")
	tok := j.Signer().Sign(uid, UnsubPurpose, j.now().Add(statement.TokenTTL))
	l := Links{Unsubscribe: base + "/api/onboarding-email/unsubscribe?t=" + tok}
	if base != "" {
		l.App = base + "/"
		l.Demo = base + "/demo"
	}
	return l
}

// send delivers one message. Best effort: the claim is already taken, so a
// failure is logged (recipient redacted) and not retried; the in-app connect
// prompt stays.
func (j *Job) send(ctx context.Context, log *slog.Logger, uid uuid.UUID, to, kind string, c Content) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	err := j.Mail.Send(ctx, email.Message{
		To: to, Subject: c.Subject, Text: c.Text,
		IdempotencyKey: c.Key + "-" + uid.String(),
		Headers: map[string]string{
			"List-Unsubscribe":      "<" + c.UnsubscribeURL + ">",
			"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		},
	})
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), to, "<recipient>")
		log.Warn("onboarding email: send failed", "kind", kind, "user_id", uid.String(), "err", msg)
		return false
	}
	log.Info("onboarding email sent", "kind", kind, "user_id", uid.String())
	if j.OnSent != nil {
		j.OnSent(kind, c.Attempt, uid)
	}
	return true
}
