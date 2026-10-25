package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/idp"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/signuprequests"
)

// passwordMinLen is the minimum accepted password length.
const passwordMinLen = 12

var (
	reUpper   = regexp.MustCompile(`[A-Z]`)
	reLower   = regexp.MustCompile(`[a-z]`)
	reDigit   = regexp.MustCompile(`[0-9]`)
	reSpecial = regexp.MustCompile(`[^A-Za-z0-9]`)
)

// validatePassword enforces the client-facing complexity rules so the
// backend and frontend agree. The matching checklist is rendered live
// in the /signup page.
func validatePassword(p string) string {
	if len(p) < passwordMinLen {
		return "password must be at least 12 characters"
	}
	for _, check := range []struct {
		re  *regexp.Regexp
		msg string
	}{
		{reUpper, "password must contain at least one uppercase letter"},
		{reLower, "password must contain at least one lowercase letter"},
		{reDigit, "password must contain at least one digit"},
		{reSpecial, "password must contain at least one special character"},
	} {
		if !check.re.MatchString(p) {
			return check.msg
		}
	}
	return ""
}

// isValidEmail is a minimal sanity-check; we do not try to be RFC-
// complete. The IdP will reject anything truly malformed anyway.
func isValidEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at < 1 || at == len(s)-1 {
		return false
	}
	for _, c := range s {
		if unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

// handleSignup — POST /api/signup (public)
//
// Body:
//
//	{
//	  "signup_token":  "ABCDEFGHIJKLMNOPQRSTUV234X",
//	  "display_name":  "Alice",        // optional
//	  "password":      "S3cur3P@ssword!"
//	}
//
// Email comes from the signup_requests row the admin approved, so
// the client doesn't supply it (and can't override it). The legacy
// invite_code path was removed in v0.18.29 once the token flow
// drained any in-flight codes.
//
// Success: 201 {"ok": true, "signed_in": bool}
// Client errors: 400 / 409 / 410
// Backend errors: 502 / 500
//
// signed_in reports whether the response also planted a Rivolt
// session cookie. The user has just proven they own the approved
// email (the token came from it) and chosen a password, so making
// them sign in again straight away only adds a step - and the old
// "wait a minute, then sign in" screen is where some signups gave up.
// issueSession may be nil (no auth configured); the SPA then falls
// back to sending the user to /login.
func handleSignup(d *sql.DB, srs *signuprequests.Store, ac idp.UserProvider, issueSession func(http.ResponseWriter, *http.Request, uuid.UUID) error, m *metrics.Metrics, log *slog.Logger, capOK func(context.Context) (bool, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SignupToken string `json:"signup_token"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}

		body.SignupToken = strings.TrimSpace(body.SignupToken)
		body.DisplayName = strings.TrimSpace(body.DisplayName)

		if body.SignupToken == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "signup_token is required"})
			return
		}
		if srs == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "token signup disabled"})
			return
		}
		tokenReq, err := srs.LookupToken(r.Context(), body.SignupToken)
		if err != nil {
			writeJSON(w, http.StatusGone, map[string]any{"error": "signup link is invalid or expired"})
			return
		}
		// A link approved automatically (no admin named) is only good while
		// there is room: the cap is checked again here, because many links
		// can be out at once. An admin's approval is exempt, as always.
		if tokenReq.DecidedBy == nil && capOK != nil {
			if ok, err := capOK(r.Context()); err != nil || !ok {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "Rivolt is at capacity right now. Your link stays valid for 14 days; please try again later."})
				return
			}
		}
		email := tokenReq.Email
		if body.DisplayName == "" {
			body.DisplayName = email
		}

		// --- Validate ---
		if !isValidEmail(email) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valid email is required"})
			return
		}
		if msg := validatePassword(body.Password); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
			return
		}

		// --- Create the user row (username = email) ---
		userID, err := db.CreateUser(r.Context(), d, email, email, body.DisplayName, "user")
		if err != nil {
			if errors.Is(err, db.ErrUserExists) {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "email already registered"})
				return
			}
			if log != nil {
				log.Error("signup: create user", "err", err.Error())
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to create user"})
			return
		}

		// --- Provision into IdP ---
		if ac != nil && ac.Enabled() {
			if err := ac.CreateUser(r.Context(), idp.CreateRequest{
				Username:    email,
				Email:       email,
				DisplayName: body.DisplayName,
				Role:        "user",
				Password:    body.Password,
			}); err != nil {
				// Roll back the rivolt row so the user can retry.
				if derr := db.DeleteUser(r.Context(), d, userID); derr != nil && log != nil {
					log.Error("signup: rollback after idp create failed",
						"id", userID.String(), "err", derr.Error())
				}
				if log != nil {
					log.Error("signup: idp create", "err", err.Error())
				}
				writeJSON(w, http.StatusBadGateway, map[string]any{"error": "account provisioning failed"})
				return
			}
		}

		// --- Consume the token ---
		// Mark the signup token used after provisioning so a failed
		// IdP create leaves the token available for the user to
		// retry. A race that double-consumes is logged but not
		// rolled back — the account is already created.
		if _, err := srs.ConsumeToken(r.Context(), body.SignupToken); err != nil && log != nil {
			log.Warn("signup: consume token after successful create",
				"request_id", tokenReq.ID.String(), "user_id", userID.String(), "err", err.Error())
		}

		recordFunnel(r.Context(), m, userID, funnelSignupCreated)
		db.RecordServerEvent(r.Context(), d, userID, db.EventSignupComplete, map[string]any{"method": "email"})

		signedIn := false
		if issueSession != nil {
			if err := issueSession(w, r, userID); err != nil {
				if log != nil {
					log.Warn("signup: issue session", "user_id", userID.String(), "err", err.Error())
				}
			} else {
				signedIn = true
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "signed_in": signedIn})
	}
}

// handleMeEnriched replaces auth.Service.Me with a version that
// additionally includes onboarding_completed from the DB. The auth
// logic (session resolution, role lookup) is unchanged; we read the
// DB row ourselves for the extra column rather than changing the auth
// package interface.
// meRole is the role /api/auth/me reports: "user" when unknown, and for an
// admin when the deployment hides the admin surface.
func meRole(role string, hideAdmin bool) string {
	if role == "" || (hideAdmin && role == "admin") {
		return "user"
	}
	return role
}

// hideAdmin (RIVOLT_ADMIN_DISABLED) reports an admin's role as "user": the
// public site has no admin surface, so the SPA mustn't link to one.
func handleMeEnriched(svc *auth.Service, d *sql.DB, hideAdmin bool) http.HandlerFunc {
	return handleMeEnrichedWith(svc, d, hideAdmin, nil, false)
}

// meResponse is GET /api/auth/me for a signed-in user. Units is the account's resolved display units (a settings.Units) when
// the server has a settings store, else absent. A visitor with no session at all gets meSignedOut instead.
type meResponse struct {
	Authenticated       bool            `json:"authenticated"`
	UserID              string          `json:"user_id"`
	Username            string          `json:"username"`
	Role                string          `json:"role"`
	OnboardingCompleted bool            `json:"onboarding_completed"`
	Terms               termsState      `json:"terms"`
	Units               *settings.Units `json:"units,omitempty"`
	// Passkeys is true when this server offers passkeys to the signed-in user (the routes under /api/auth/passkeys exist), so
	// Settings shows the card only where it can work. Absent (false) where passkeys are off, such as prod today.
	Passkeys bool `json:"passkeys,omitempty"`
}

// meSignedOut is the answer for a visitor with no session credential at all.
type meSignedOut struct {
	Authenticated bool `json:"authenticated"`
}

// handleMeEnrichedWith is handleMeEnriched plus `units`, the account's resolved display units, when unitsFor is set, and `passkeys`.
func handleMeEnrichedWith(svc *auth.Service, d *sql.DB, hideAdmin bool, unitsFor func(ctx context.Context, uid uuid.UUID) *settings.Units, passkeys bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			// A visitor with no session at all is not an error: the
			// landing page and the login page probe this endpoint
			// signed out, and a 401 there is console noise. Answer 200
			// {"authenticated": false}. A request that DID present a
			// session credential which didn't resolve (expired or
			// invalid) keeps the 401, so "your session expired" logic,
			// the Kratos-session-without-account signal and the
			// landing gate still see it.
			if !svc.HasCredentials(r) {
				// Never cacheable: unlike the old 401 this is a 200, and
				// a CDN or browser cache rule must not pin "signed out"
				// for a visitor who signs in a moment later.
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusOK, meSignedOut{})
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Reuse the exported Me logic to build the base response, but since
		// Me writes directly to w we can't intercept it. Inline the same
		// three lookups (username, role, onboarding_completed) here.
		var username string
		if d != nil {
			if un, err := db.LookupUsername(r.Context(), d, uid); err == nil {
				username = un
			}
		}
		role, _ := db.RoleFor(r.Context(), d, uid)
		role = meRole(role, hideAdmin)
		onboardingDone, _ := db.OnboardingCompleted(r.Context(), d, uid)
		w.Header().Set("Content-Type", "application/json")
		me := meResponse{
			Authenticated:       true,
			UserID:              uid.String(),
			Username:            username,
			Role:                role,
			OnboardingCompleted: onboardingDone,
			Terms:               termsView(r.Context(), d, uid),
			Passkeys:            passkeys,
		}
		if unitsFor != nil {
			me.Units = unitsFor(r.Context(), uid)
		}
		_ = json.NewEncoder(w).Encode(me)
	}
}

// handleOnboardingComplete — POST /api/onboarding/complete
//
// Marks the current user's onboarding as finished. The frontend calls
// this when the user leaves the connect step, either connected or via
// "I'll connect later" - the funnel records which.
func handleOnboardingComplete(d *sql.DB, m *metrics.Metrics) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if err := db.SetOnboardingCompleted(r.Context(), d, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		var vehicles int
		if err := d.QueryRowContext(r.Context(),
			`SELECT COUNT(*) FROM vehicles WHERE user_id = $1`, uid,
		).Scan(&vehicles); err == nil && vehicles == 0 {
			recordFunnel(r.Context(), m, uid, funnelOnboardingSkipped)
		}
		recordFunnel(r.Context(), m, uid, funnelOnboardingComplete, "vehicles", vehicles)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// signupSessionIssuer adapts the auth service for handleSignup,
// returning nil when auth isn't configured so the handler skips the
// sign-in rather than calling a nil service.
func signupSessionIssuer(a *auth.Service) func(http.ResponseWriter, *http.Request, uuid.UUID) error {
	if a == nil {
		return nil
	}
	return a.IssueSession
}
