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
	"github.com/apohor/rivolt/internal/onboardmail"
	"github.com/apohor/rivolt/internal/statement"
)

// The welcome email and setup reminders (internal/onboardmail) can be turned
// off in Settings or with the signed one-click link in each email. Turning
// them off never touches the monthly statement email or account-security
// mail.

type onboardingEmailAPI struct {
	db     *sql.DB
	signer *statement.Signer
	now    func() time.Time
}

// GET /api/settings/onboarding-email -> {"enabled": bool}
func (a *onboardingEmailAPI) getSettings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	off, err := db.EngagementEmailsOff(r.Context(), a.db, uid)
	if err != nil {
		serverError(w, r, "onboarding email settings", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": !off})
}

// PUT /api/settings/onboarding-email {"enabled": bool}
func (a *onboardingEmailAPI) putSettings(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	// A choice about the user's own mail is theirs, never an admin's while
	// viewing as them.
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
	if err := db.SetEngagementEmailsOff(r.Context(), a.db, uid, !*body.Enabled); err != nil {
		serverError(w, r, "onboarding email settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": *body.Enabled})
}

const onboardingUnsubExpired = `<h1>This link has expired</h1><p>Sign in to Rivolt and change these emails in Settings → Notifications.</p>`

// verify accepts only a token signed for these emails (the statement
// email's tokens carry a YYYY-MM period and are refused here).
func (a *onboardingEmailAPI) verify(r *http.Request) (uuid.UUID, bool) {
	uid, purpose, err := a.signer.Verify(r.URL.Query().Get("t"), a.now())
	if err != nil || purpose != onboardmail.UnsubPurpose {
		return uuid.Nil, false
	}
	return uid, true
}

// GET shows a confirmation page whose form POSTs, so link scanners that only
// GET never unsubscribe anyone.
func (a *onboardingEmailAPI) unsubscribeGet(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.verify(r); !ok {
		writeUnsubPage(w, http.StatusBadRequest, onboardingUnsubExpired)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>Stop the welcome and setup emails?</h1>
<form method="post" action="/api/onboarding-email/unsubscribe?t=`+html.EscapeString(r.URL.Query().Get("t"))+`"><button type="submit">Stop these emails</button></form>`)
}

// POST performs it (RFC 8058 one-click posts here too). Idempotent.
func (a *onboardingEmailAPI) unsubscribePost(w http.ResponseWriter, r *http.Request) {
	uid, ok := a.verify(r)
	if !ok {
		writeUnsubPage(w, http.StatusBadRequest, onboardingUnsubExpired)
		return
	}
	if err := db.SetEngagementEmailsOff(r.Context(), a.db, uid, true); err != nil {
		serverError(w, r, "onboarding email unsubscribe", err)
		return
	}
	writeUnsubPage(w, http.StatusOK, `<h1>You won't get these emails</h1><p>That covers the welcome and setup reminders. You can turn them back on in Settings → Notifications.</p>`)
}
