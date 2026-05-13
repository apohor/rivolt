package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// httpStatusForUpstream picks an HTTP status for an error that
// originated from the Rivian gateway. The mapping matters for two
// reasons:
//
//  1. Cloudflare (and most edges) replace 5xx response bodies with
//     a branded HTML error page. A bad-credentials response that
//     comes back as 502 reaches the browser as Cloudflare HTML
//     instead of our JSON, so the SPA can't render a useful
//     message. 4xx is passed through cleanly.
//  2. The class already encodes who the error belongs to. UserAction
//     means the user has to fix something on their side (bad
//     password, missing MFA, expired session). RateLimited means
//     the client should back off. Mapping these to 4xx gives the
//     SPA accurate semantics without inspecting our error strings.
//
// 5xx is reserved for genuine upstream gateway failures the user
// cannot fix.
func httpStatusForUpstream(err error) int {
	var ue *rivian.UpstreamError
	if !errors.As(err, &ue) {
		return http.StatusBadGateway
	}
	switch ue.Class {
	case rivian.ClassUserAction:
		// 401 — the user's credentials / session need attention.
		// Distinct from a rivolt-side auth fail (also 401) by
		// the response body, which carries the upstream reason.
		return http.StatusUnauthorized
	case rivian.ClassRateLimited:
		return http.StatusTooManyRequests
	case rivian.ClassOutage:
		return http.StatusServiceUnavailable
	default:
		// ClassTransient + ClassUnknown: real upstream wobble,
		// retry-eligible. 502 is the right verb here.
		return http.StatusBadGateway
	}
}

// writeUpstreamError renders an UpstreamError (or any wrapped
// error from the rivian package) as JSON with the right status.
// Body shape is stable: {error, class, reason?}.
func writeUpstreamError(w http.ResponseWriter, err error) {
	status := httpStatusForUpstream(err)
	body := map[string]any{"error": err.Error()}
	var ue *rivian.UpstreamError
	if errors.As(err, &ue) {
		body["class"] = ue.Class.String()
		if ue.Reason != "" {
			body["reason"] = ue.Reason
		}
	}
	writeJSON(w, status, body)
}

// rivianStatusDTO is the public view of the Rivian account state.
// Email is returned as-is for the authenticated caller's own session.
type rivianStatusDTO struct {
	Enabled       bool   `json:"enabled"` // true iff a live client is wired
	Authenticated bool   `json:"authenticated"`
	MFAPending    bool   `json:"mfa_pending"`
	Email         string `json:"email,omitempty"`
	// NeedsReauth signals that a stored session is structurally
	// present (Authenticated=true) but a runtime classifier has
	// flagged it as no longer usable — typically because Rivian's
	// WS gateway is rejecting the userSessionToken even though the
	// REST cache still hides the rot. UI surfaces a banner so the
	// user knows to re-sign in instead of waiting for missing
	// drives to tip them off.
	NeedsReauth       bool   `json:"needs_reauth"`
	NeedsReauthReason string `json:"needs_reauth_reason,omitempty"`
}

func handleRivianStatus(reg rivian.AccountRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			writeJSON(w, http.StatusOK, rivianStatusDTO{Enabled: false})
			return
		}
		// Status is allowed without an authenticated context (e.g.
		// the SPA polling on first paint before a session resolves);
		// in that case we return the "live wired but no session yet"
		// shape rather than 401 so the UI render path stays simple.
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			writeJSON(w, http.StatusOK, rivianStatusDTO{Enabled: true})
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			writeJSON(w, http.StatusOK, rivianStatusDTO{Enabled: false})
			return
		}
		needs, reason := lc.NeedsReauth()
		writeJSON(w, http.StatusOK, rivianStatusDTO{
			Enabled:           true,
			Authenticated:     lc.Authenticated(),
			MFAPending:        lc.MFAPending(),
			Email:             lc.Email(),
			NeedsReauth:       needs,
			NeedsReauthReason: reason,
		})
	}
}

type rivianLoginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func handleRivianLogin(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry, mailer *email.Client, d *sql.DB, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		var req rivianLoginReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.Email = strings.TrimSpace(req.Email)
		if req.Email == "" || req.Password == "" {
			http.Error(w, "email and password required", http.StatusBadRequest)
			return
		}
		err := lc.Login(r.Context(), rivian.Credentials{Email: req.Email, Password: req.Password})
		switch {
		case errors.Is(err, rivian.ErrMFARequired):
			writeJSON(w, http.StatusOK, map[string]any{
				"authenticated": false,
				"mfa_pending":   true,
			})
			return
		case err != nil:
			// Log every login failure at WARN so the gate or upstream
			// class is visible without round-tripping the response body.
			fields := []any{"err", err.Error()}
			var ue *rivian.UpstreamError
			if errors.As(err, &ue) {
				fields = append(fields,
					"class", ue.Class.String(),
					"op", ue.Op,
					"http_status", ue.HTTPStatus,
					"ext_code", ue.ExtCode,
					"reason", ue.Reason,
				)
			}
			slog.WarnContext(r.Context(), "rivian login failed", fields...)
			writeUpstreamError(w, err)
			return
		}
		// First-connect detection BEFORE the persist: if the user has
		// no stored session yet, this login is their initial Rivian
		// connection — notify the admin once it lands. Re-logins (token
		// rotation, password change) skip the notification.
		var hadSession bool
		if existing, lerr := secrets.LoadRivianSession(r.Context(), store, uid); lerr == nil {
			hadSession = existing.UserSessionToken != ""
		}
		// Fully authenticated — persist.
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, lc.Snapshot()); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
		}
		if !hadSession {
			username := ""
			if d != nil {
				if u, derr := db.LookupUsername(r.Context(), d, uid); derr == nil {
					username = u
				}
			}
			go notifyAdmin(context.Background(), mailer, logger,
				"Rivolt user connected Rivian account",
				"A user finished the Rivian sign-in step:\n\n"+
					"  Rivolt user: "+username+" ("+uid.String()+")\n"+
					"  Rivian email: "+lc.Email()+"\n\n"+
					"Vehicles, drives, and charges should start appearing\n"+
					"on the admin page within a few seconds.\n",
			)
		}
		// Start (or no-op resume of) this user's StateMonitor so
		// the recorder + WS subscription run under their identity.
		if monitors != nil {
			monitors.Start(r.Context(), uid)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"email":         lc.Email(),
		})
	}
}

type rivianMFAReq struct {
	OTP string `json:"otp"`
}

func handleRivianMFA(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		if !lc.MFAPending() {
			http.Error(w, "no MFA challenge in flight; start with /login", http.StatusConflict)
			return
		}
		var req rivianMFAReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.OTP = strings.TrimSpace(req.OTP)
		if req.OTP == "" {
			http.Error(w, "otp required", http.StatusBadRequest)
			return
		}
		// Second leg of the MFA dance. Email is read from the
		// pending-state cached inside the client.
		if err := lc.Login(r.Context(), rivian.Credentials{OTP: req.OTP}); err != nil {
			fields := []any{"err", err.Error()}
			var ue *rivian.UpstreamError
			if errors.As(err, &ue) {
				fields = append(fields,
					"class", ue.Class.String(),
					"op", ue.Op,
					"http_status", ue.HTTPStatus,
					"ext_code", ue.ExtCode,
					"reason", ue.Reason,
				)
			}
			slog.WarnContext(r.Context(), "rivian mfa failed", fields...)
			writeUpstreamError(w, err)
			return
		}
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, lc.Snapshot()); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
		}
		if monitors != nil {
			monitors.Start(r.Context(), uid)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": true,
			"email":         lc.Email(),
		})
	}
}

func handleRivianLogout(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reg == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		uid, ok := auth.UserFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		lc := reg.For(uid)
		if lc == nil {
			http.Error(w, "live rivian client not configured", http.StatusNotFound)
			return
		}
		lc.Logout()
		if monitors != nil {
			monitors.Stop(uid)
		}
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, rivian.Session{}); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": false})
	}
}
