package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/apohor/rivolt/internal/hydra"
	"github.com/apohor/rivolt/internal/kratos"
)

// hydraDeps is the small set of dependencies the Hydra OIDC bridge
// handlers need. Kept private to the api package; main.go injects
// the clients via the existing Config struct in api.go.
type hydraDeps struct {
	Hydra  *hydra.Client
	Kratos *kratos.Client
	Logger *slog.Logger
}

// hydraLoginGET handles GET /api/auth/hydra/login?login_challenge=…
//
// Two outcomes:
//
//	skip=true → POST our accept immediately, 302 to redirect_to.
//	         (Hydra has already authenticated the user via a prior
//	          session; we just need to confirm the subject.)
//
//	skip=false → return JSON with the challenge id, client name,
//	         and requested scopes so the SPA login page can render
//	         a password prompt that POSTs back to us.
func hydraLoginGET(d hydraDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Hydra == nil || !d.Hydra.Enabled() {
			http.Error(w, "hydra not configured", http.StatusNotFound)
			return
		}
		challenge := strings.TrimSpace(r.URL.Query().Get("login_challenge"))
		if challenge == "" {
			http.Error(w, "missing login_challenge", http.StatusBadRequest)
			return
		}
		req, err := d.Hydra.GetLoginRequest(r.Context(), challenge)
		if err != nil {
			d.Logger.Error("hydra login: get challenge", "err", err)
			http.Error(w, "hydra fetch failed", http.StatusBadGateway)
			return
		}
		if req.Skip {
			redirect, err := d.Hydra.AcceptLoginRequest(r.Context(), challenge,
				hydra.AcceptLoginRequest{
					Subject:     req.Subject,
					Remember:    true,
					RememberFor: 3600,
				})
			if err != nil {
				d.Logger.Error("hydra login: skip-accept failed", "err", err)
				http.Error(w, "hydra accept failed", http.StatusBadGateway)
				return
			}
			http.Redirect(w, r, redirect.RedirectTo, http.StatusFound)
			return
		}
		// Not skipped. The SPA renders the form; we hand back enough
		// metadata to populate it. The challenge id is the secret
		// that ties the eventual POST to this Hydra request — it's
		// already in the URL Hydra redirected the user to, so we
		// don't need to plant any cookie ourselves.
		writeJSON(w, http.StatusOK, hydraLoginGetResponse{
			Challenge:      challenge,
			ClientID:       req.Client.ClientID,
			ClientName:     orFallback(req.Client.ClientName, req.Client.ClientID),
			RequestedScope: req.RequestedScope,
			LoginHint:      req.OIDCContext.LoginHint,
		})
	}
}

// hydraLoginGetResponse is the JSON the SPA renders into a form.
type hydraLoginGetResponse struct {
	Challenge      string   `json:"challenge"`
	ClientID       string   `json:"client_id"`
	ClientName     string   `json:"client_name"`
	RequestedScope []string `json:"requested_scope"`
	LoginHint      string   `json:"login_hint,omitempty"`
}

// hydraLoginPOST handles POST /api/auth/hydra/login. Body:
// {"challenge":"…","email":"…","password":"…"}.
//
// Authenticates against Kratos's public API (no cookie/CSRF), and
// on success accepts the login on Hydra with the Kratos identity
// id as the subject. Responds with {"redirect_to":"…"} so the SPA
// can window.location to it (a 302 here would be invisible to fetch).
func hydraLoginPOST(d hydraDeps) http.HandlerFunc {
	type inT struct {
		Challenge string `json:"challenge"`
		Email     string `json:"email"`
		Password  string `json:"password"`
	}
	type outT struct {
		RedirectTo string `json:"redirect_to"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Hydra == nil || !d.Hydra.Enabled() {
			http.Error(w, "hydra not configured", http.StatusNotFound)
			return
		}
		if d.Kratos == nil || !d.Kratos.Enabled() {
			http.Error(w, "kratos not configured", http.StatusNotFound)
			return
		}
		var in inT
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		in.Email = strings.TrimSpace(in.Email)
		if in.Challenge == "" || in.Email == "" || in.Password == "" {
			http.Error(w, "challenge, email, password required", http.StatusBadRequest)
			return
		}

		identity, err := d.Kratos.LoginByPassword(r.Context(), in.Email, in.Password)
		if err != nil {
			if errors.Is(err, kratos.ErrInvalidCredentials) {
				// Constant-time-ish: same response for "no such user"
				// and "wrong password". Don't log the email at info
				// level — it's PII-adjacent.
				http.Error(w, "invalid credentials", http.StatusUnauthorized)
				return
			}
			d.Logger.Error("hydra login: kratos auth", "err", err)
			http.Error(w, "auth failed", http.StatusBadGateway)
			return
		}

		redirect, err := d.Hydra.AcceptLoginRequest(r.Context(), in.Challenge,
			hydra.AcceptLoginRequest{
				Subject:     identity.ID,
				Remember:    true,
				RememberFor: 3600,
				ACR:         "0",
				AMR:         []string{"pwd"},
			})
		if err != nil {
			d.Logger.Error("hydra login: accept", "err", err, "subject", identity.ID)
			http.Error(w, "hydra accept failed", http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, outT{RedirectTo: redirect.RedirectTo})
	}
}

// hydraConsentGET handles GET /api/auth/hydra/consent?consent_challenge=…
// First-party consent: every requested scope is granted. Hydra
// remembers the grant for the session lifetime so a returning user
// never sees a consent prompt.
func hydraConsentGET(d hydraDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Hydra == nil || !d.Hydra.Enabled() {
			http.Error(w, "hydra not configured", http.StatusNotFound)
			return
		}
		challenge := strings.TrimSpace(r.URL.Query().Get("consent_challenge"))
		if challenge == "" {
			http.Error(w, "missing consent_challenge", http.StatusBadRequest)
			return
		}
		req, err := d.Hydra.GetConsentRequest(r.Context(), challenge)
		if err != nil {
			d.Logger.Error("hydra consent: get challenge", "err", err)
			http.Error(w, "hydra fetch failed", http.StatusBadGateway)
			return
		}
		redirect, err := d.Hydra.AcceptConsentRequest(r.Context(), challenge,
			hydra.AcceptConsentRequest{
				GrantScope:               req.RequestedScope,
				GrantAccessTokenAudience: req.RequestedAccessTokenAudience,
				Remember:                 true,
				RememberFor:              3600,
			})
		if err != nil {
			d.Logger.Error("hydra consent: accept", "err", err)
			http.Error(w, "hydra accept failed", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, redirect.RedirectTo, http.StatusFound)
	}
}

// orFallback returns a if a is non-empty, else b.
func orFallback(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
