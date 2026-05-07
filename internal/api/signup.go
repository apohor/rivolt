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
	"github.com/apohor/rivolt/internal/invites"
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
// complete. Authelia will reject anything truly malformed anyway.
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
//	  "invite_code":   "ABCDEFGHIJKLMNOPQRST",
//	  "email":         "alice@example.com",
//	  "display_name":  "Alice",          // optional, defaults to email
//	  "password":      "S3cur3P@ssword!"
//	}
//
// Success: 201 {"ok": true}
// Client errors: 400 / 409
// Backend errors: 502 / 500
func handleSignup(d *sql.DB, inv *invites.Store, ac idp.UserProvider, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			InviteCode  string `json:"invite_code"`
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			Password    string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body"})
			return
		}

		body.Email = strings.ToLower(strings.TrimSpace(body.Email))
		body.InviteCode = strings.TrimSpace(body.InviteCode)
		body.DisplayName = strings.TrimSpace(body.DisplayName)
		if body.DisplayName == "" {
			body.DisplayName = body.Email
		}

		// --- Validate ---
		if body.InviteCode == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invite_code is required"})
			return
		}
		if !isValidEmail(body.Email) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "valid email is required"})
			return
		}
		if msg := validatePassword(body.Password); msg != "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": msg})
			return
		}

		// --- Check invite code (pre-flight; the actual redeem is post-create) ---
		if err := inv.Validate(r.Context(), body.InviteCode); err != nil {
			if errors.Is(err, invites.ErrInvalidCode) {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid or already-used invite code"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "invite validation failed"})
			return
		}

		// --- Create the user row (username = email) ---
		userID, err := db.CreateUser(r.Context(), d, body.Email, body.Email, body.DisplayName, "user")
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
				Username:    body.Email,
				Email:       body.Email,
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

		// --- Redeem invite code ---
		// Redeem after provisioning; if Authelia fails we rolled back so the
		// code remains available for the next attempt. If Redeem itself fails
		// (extremely unlikely race) the user is created and provisioned — not
		// worth rolling back for that; log and continue.
		if err := inv.Redeem(r.Context(), body.InviteCode, userID); err != nil && log != nil {
			log.Warn("signup: redeem invite code after successful create",
				"code", body.InviteCode, "user_id", userID.String(), "err", err.Error())
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

// handleAdminInviteCodesCreate — POST /api/admin/invite-codes
//
// Body: {"count": 1}   (count 1–100, default 1)
// Returns: {"codes": ["ABCDE…", …]}
func handleAdminInviteCodesCreate(d *sql.DB, inv *invites.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
			return
		}
		var body struct {
			Count int `json:"count"`
		}
		body.Count = 1
		if r.ContentLength > 0 {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		if body.Count < 1 {
			body.Count = 1
		}
		codes, err := inv.Generate(r.Context(), uid, body.Count)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"codes": codes})
	}
}

// handleAdminInviteCodesList — GET /api/admin/invite-codes
//
// Returns all codes, most-recently-created first.
func handleAdminInviteCodesList(inv *invites.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list, err := inv.List(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"codes": list})
	}
}
