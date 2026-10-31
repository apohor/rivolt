package api

import (
	"database/sql"
	"html"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/r2arrival"
	"github.com/apohor/rivolt/internal/statement"
)

// The R2 arrival-month email (internal/r2arrival) carries a signed one-click link that deletes the reminder. The month itself is set,
// changed and deleted in Settings through /api/settings/engagement (r2_arrival_month); the link is the same delete, for a user who
// is not signed in. It touches nothing else: not the other emails, not the account.

type r2ArrivalEmailAPI struct {
	db     *sql.DB
	signer *statement.Signer
	now    func() time.Time
}

const r2ArrivalUnsubExpired = `<h1>This link has expired</h1><p>Sign in to Rivolt and change or delete the R2 reminder in Settings.</p>`

// verify accepts only a token signed for this reminder.
func (a *r2ArrivalEmailAPI) verify(r *http.Request) (uuid.UUID, bool) {
	uid, purpose, err := a.signer.Verify(r.URL.Query().Get("t"), a.now())
	if err != nil || purpose != r2arrival.UnsubPurpose {
		return uuid.Nil, false
	}
	return uid, true
}

// GET shows a confirmation page whose form POSTs, so link scanners that only GET delete nobody's reminder.
func (a *r2ArrivalEmailAPI) unsubscribeGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.verify(r); !ok {
		writeUnsubPage(w, http.StatusBadRequest, r2ArrivalUnsubExpired)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>Delete the R2 reminder?</h1>
<form method="post" action="/api/r2-arrival/unsubscribe?t=`+html.EscapeString(r.URL.Query().Get("t"))+`"><button type="submit">Delete the reminder</button></form>`)
}

// POST performs it (RFC 8058 one-click posts here too). Idempotent.
func (a *r2ArrivalEmailAPI) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.verify(r)
	if !ok {
		writeUnsubPage(w, http.StatusBadRequest, r2ArrivalUnsubExpired)
		return
	}
	if err := db.ClearR2ArrivalMonth(r.Context(), a.db, uid); err != nil {
		serverError(w, r, "r2 arrival unsubscribe", err)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>Reminder removed</h1><p>We won't send the R2 reminder. You can set a month again in Settings.</p>`)
}
