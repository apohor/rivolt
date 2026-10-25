package api

import (
	"context"
	"net/http"
	"sync"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// The Terms gate, server side. The web client shows an "I agree" screen to a signed-in user who must accept the current Terms
// (termsRequired), but a screen is not a gate: the data routes answered 200 to any signed-in user, and the iPhone app never
// sees the screen. This middleware answers 403 {"error": "terms_required"} on every route behind a session until the user has
// accepted, with the same rule the client uses (termsRequired: an account created on or after termsEffectiveAt that has not
// accepted currentTermsVersion; older accounts are never gated).
//
// Still open to such a user, so they can always agree, take their data and leave, or stop being notified: the accept endpoint, the
// two ways to download their own data (export and backup), deleting the account, and switching push notifications off. Authentication routes (/api/auth/*, including me
// and sign-out) and the public routes are outside the gated groups. An admin viewing as a user is not gated (the client
// does not gate the view either: the admin has accepted nothing for them), and neither is the public demo user.
const termsRequiredError = "terms_required"

// termsRequiredBody is the 403 answer: {"error": "terms_required", "current": "<version>"}.
type termsRequiredBody struct {
	Error   string `json:"error"`
	Current string `json:"current"`
}

// termsGateExempt are the routes a user who has not accepted the Terms can still call.
func termsGateExempt(r *http.Request) bool {
	switch r.Method + " " + r.URL.Path {
	case "POST /api/terms/accept", "GET /api/export", "GET /api/data/backup", "DELETE /api/data/account",
		// Switching notifications off: someone declining new Terms can still stop pushes (turning them on stays gated).
		"POST /api/push/off", "POST /api/push/unsubscribe", "POST /api/push/apns/unregister":
		return true
	}
	return false
}

// termsLookup reads what a user accepted (db.TermsFor, or a test's).
type termsLookup func(ctx context.Context, uid uuid.UUID) (db.Terms, error)

// requireTermsMW is the gate. A user found to need no acceptance (accepted, or an account the gate never applies to) is
// remembered for this version, so the common request costs no query; a user who must accept is looked up every time, which is
// cheap and keeps acceptance effective at once.
func requireTermsMW(lookup termsLookup) func(http.Handler) http.Handler {
	var mu sync.Mutex
	cleared := map[uuid.UUID]string{} // user -> the Terms version they were last found clear for
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := auth.UserFromContext(r.Context())
			// The anonymous public demo runs as the demo user, who accepts nothing and is never asked to: exempt it here, not by
			// the luck of its creation date against termsEffectiveAt.
			if !ok || lookup == nil || isDemoUser(uid) || termsGateExempt(r) || auth.IsImpersonating(r.Context()) {
				next.ServeHTTP(w, r)
				return
			}
			mu.Lock()
			known := cleared[uid] == currentTermsVersion
			mu.Unlock()
			if known {
				next.ServeHTTP(w, r)
				return
			}
			t, err := lookup(r.Context(), uid)
			if err != nil {
				http.Error(w, "terms check unavailable", http.StatusServiceUnavailable)
				return
			}
			if termsRequired(t) {
				writeJSON(w, http.StatusForbidden, termsRequiredBody{Error: termsRequiredError, Current: currentTermsVersion})
				return
			}
			mu.Lock()
			cleared[uid] = currentTermsVersion
			mu.Unlock()
			next.ServeHTTP(w, r)
		})
	}
}

// termsLookupFor is the gate's lookup for these Deps: the test hook when set, else the database; nil (no gate) with neither.
func termsLookupFor(d Deps) termsLookup {
	if d.TermsLookup != nil {
		return d.TermsLookup
	}
	if d.DB == nil {
		return nil
	}
	return func(ctx context.Context, uid uuid.UUID) (db.Terms, error) { return db.TermsFor(ctx, d.DB, uid) }
}
