package api

import (
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
// Success: 201 {"ok": true}
// Client errors: 400 / 409 / 410
// Backend errors: 502 / 500
func handleSignup(d *sql.DB, srs *signuprequests.Store, ac idp.UserProvider, log *slog.Logger) http.HandlerFunc {
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

		writeJSON(w, http.StatusCreated, map[string]any{"ok": true})
	}
}

// handleMeEnriched replaces auth.Service.Me with a version that
// additionally includes onboarding_completed from the DB. The auth
// logic (session resolution, role lookup) is unchanged; we read the
// DB row ourselves for the extra column rather than changing the auth
// package interface.
func handleMeEnriched(svc *auth.Service, d *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// Reuse the exported Me logic to build the base response, but since
		// Me writes directly to w we can't intercept it. Inline the same
		// three lookups (username, role, onboarding_completed) here.
		var username string
		if un, err := db.LookupUsername(r.Context(), d, uid); err == nil {
			username = un
		}
		role, _ := db.RoleFor(r.Context(), d, uid)
		if role == "" {
			role = "user"
		}
		onboardingDone, _ := db.OnboardingCompleted(r.Context(), d, uid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user_id":               uid.String(),
			"username":              username,
			"role":                  role,
			"onboarding_completed": onboardingDone,
		})
	}
}

// handleOnboardingComplete — POST /api/onboarding/complete
//
// Marks the current user's onboarding stepper as finished. The
// frontend calls this when the user reaches the last step and clicks
// "Get started".
func handleOnboardingComplete(d *sql.DB) func(uuid.UUID, http.ResponseWriter, *http.Request) {
	return func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		if err := db.SetOnboardingCompleted(r.Context(), d, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

