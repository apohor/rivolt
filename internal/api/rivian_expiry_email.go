package api

import (
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivianexpiry"
	"github.com/apohor/rivolt/internal/statement"
)

// The reminders that say the Rivian connection is about to expire (internal/rivianexpiry) can
// be stopped in Settings or with the signed one-click link in each email. Stopping them never
// touches the reconnect email sent when the connection has already failed, the welcome and
// setup emails, the monthly statement, or account-security mail.

type rivianExpiryEmailAPI struct {
	db     *sql.DB
	signer *statement.Signer
	now    func() time.Time
}

// GET /api/settings/rivian-expiry-email -> {"enabled": bool}
func (a *rivianExpiryEmailAPI) getSettings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	off, err := db.RivianExpiryEmailsOff(r.Context(), a.db, uid)
	if err != nil {
		serverError(w, r, "rivian expiry email settings", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": !off})
}

// PUT /api/settings/rivian-expiry-email {"enabled": bool}
func (a *rivianExpiryEmailAPI) putSettings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	// A choice about the user's own mail is theirs, never an admin's while viewing as them.
	if auth.IsImpersonating(r.Context()) {
		http.Error(w, "not available while viewing as another user", http.StatusForbidden)
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&body); err != nil || body.Enabled == nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if err := db.SetRivianExpiryEmailsOff(r.Context(), a.db, uid, !*body.Enabled); err != nil {
		serverError(w, r, "rivian expiry email settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *body.Enabled})
}

const rivianExpiryUnsubExpired = `<h1>This link has expired</h1><p>Sign in to Rivolt and change these reminders in Settings → Notifications.</p>`

// verify accepts only a token signed for these reminders.
func (a *rivianExpiryEmailAPI) verify(r *http.Request) (uuid.UUID, bool) {
	uid, purpose, err := a.signer.Verify(r.URL.Query().Get("t"), a.now())
	if err != nil || purpose != rivianexpiry.UnsubPurpose {
		return uuid.Nil, false
	}
	return uid, true
}

// GET shows a confirmation page whose form POSTs, so link scanners that only GET never stop
// anyone's reminders.
func (a *rivianExpiryEmailAPI) unsubscribeGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.verify(r); !ok {
		writeUnsubPage(w, http.StatusBadRequest, rivianExpiryUnsubExpired)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>Stop the Rivian connection reminders?</h1>
<form method="post" action="/api/rivian-expiry-email/unsubscribe?t=`+html.EscapeString(r.URL.Query().Get("t"))+`"><button type="submit">Stop these reminders</button></form>`)
}

// POST performs it (RFC 8058 one-click posts here too). Idempotent.
func (a *rivianExpiryEmailAPI) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.verify(r)
	if !ok {
		writeUnsubPage(w, http.StatusBadRequest, rivianExpiryUnsubExpired)
		return
	}
	if err := db.SetRivianExpiryEmailsOff(r.Context(), a.db, uid, true); err != nil {
		serverError(w, r, "rivian expiry email unsubscribe", err)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>You won't get these reminders</h1><p>That covers the heads-up before your Rivian connection expires. If it does expire, Rivolt still tells you recording has stopped. You can turn the reminders back on in Settings → Notifications.</p>`)
}
