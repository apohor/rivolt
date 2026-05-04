package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

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

func handleRivianLogin(reg rivian.AccountRegistry, store *secrets.Store, monitors *rivian.MonitorRegistry) http.HandlerFunc {
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
			return
		}
		// Fully authenticated — persist.
		if perr := secrets.SaveRivianSession(r.Context(), store, uid, lc.Snapshot()); perr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": perr.Error()})
			return
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
			writeJSON(w, http.StatusBadGateway, map[string]any{"error": err.Error()})
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
