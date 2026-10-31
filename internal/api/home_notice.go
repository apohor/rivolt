package api

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/sessions"
)

// requestSource says where a settings write came from, for the audit line: "app" for the iPhone app's session, else "web".
func requestSource(r *http.Request) string {
	if ref, ok := auth.SessionFromContext(r.Context()); ok && ref.Kind == sessions.KindApp {
		return "app"
	}
	return "web"
}

// homeChangeNotice emails the account when its home location was changed from the iPhone app: the one sensitive write a
// leaked phone token can make. The message carries no coordinates or address. At most one a hour per account on this pod, so
// adjusting the pin a few times is one email, not five. Best effort: a failure is only logged.
type homeChangeNotice struct {
	mail interface {
		Send(ctx context.Context, m email.Message) error
	}
	db *sql.DB
	// claims makes the notice once per account per hour across every replica (engagement_sends); nil: this pod's own limit only (tests).
	claims engage.Store
	now    func() time.Time
	last   sync.Map // uuid.UUID -> time.Time
	// lookup replaces the database read of the account's verified address (tests).
	lookup func(ctx context.Context, uid uuid.UUID) (string, error)
}

const homeNoticeEvery = time.Hour

// homeNoticeKind is the engagement kind of the notice. It is a security notice, so it is claimed as a utility (outside the budget and
// quiet hours) and only deduplicated: the key is the hour, so a change on one pod and another a minute later on a second pod is one email.
const homeNoticeKind = "home_changed"

func (n *homeChangeNotice) address(ctx context.Context, uid uuid.UUID) (string, error) {
	if n.lookup != nil {
		return n.lookup(ctx, uid)
	}
	var e sql.NullString
	err := n.db.QueryRowContext(ctx, `SELECT email FROM users WHERE id = $1 AND email_verified AND NOT email_undeliverable`, uid).Scan(&e)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return e.String, err
}

// afterHomeChange is called after a successful home write (set, clear or restore) with the request that made it.
func (n *homeChangeNotice) afterHomeChange(r *http.Request) {
	if n == nil || n.mail == nil || requestSource(r) != "app" {
		return
	}
	uid, ok := auth.UserFromContext(r.Context())
	if !ok {
		return
	}
	now := time.Now
	if n.now != nil {
		now = n.now
	}
	if v, ok := n.last.Load(uid); ok && now().Sub(v.(time.Time)) < homeNoticeEvery {
		return
	}
	n.last.Store(uid, now())
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Second)
	defer cancel()
	to, err := n.address(ctx, uid)
	if err != nil || to == "" {
		return
	}
	status := "sent"
	if n.claims != nil {
		msg := engage.Msg{User: uid, Kind: homeNoticeKind, Key: now().UTC().Format("2006010215"), Channel: engage.Email, Utility: true}
		claimed, _, err := n.claims.Claim(ctx, msg, now(), now().Add(-engage.BudgetWindow), engage.MonthlyBudget)
		if err == nil && !claimed {
			return // another replica (or an earlier change this hour) sent it
		}
		// A claim that fails is sent anyway: the notice is the point, and the mail's idempotency key still dedupes within the hour.
		if err == nil {
			defer func() {
				_ = n.claims.Mark(ctx, uid, homeNoticeKind, msg.Key, status, now())
			}()
		}
	}
	if err := n.mail.Send(ctx, email.Message{
		To:             to,
		Subject:        "Your home location was changed from the iPhone app",
		IdempotencyKey: "home-changed-" + uid.String() + "-" + now().UTC().Format("2006010215"),
		Text: "Your home location on Rivolt was changed from the iPhone app.\n\n" +
			"If this was you, there is nothing to do. If it wasn't, sign in on the web, restore your previous home in Settings,\n" +
			"sign out your other devices, and write to support@rivolt.dev.\n",
	}); err != nil {
		status = "failed"
	}
}
