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

// hydraLoginGET handles GET /api/auth/hydra/login?login_challenge=….
// Always returns JSON — this endpoint is fetched by the SPA via XHR,
// not visited by the browser, so a 302 here would be silently
// swallowed by `fetch(redirect: 'follow')` and end with the SPA
// trying to JSON-parse Hydra's HTML auth page.
//
// Two shapes:
//
//	skip=true  → {"skip": true, "redirect_to": "…"}.  The SPA
//	             does window.location.assign(redirect_to) without
//	             rendering a form.  Used when Hydra remembered a
//	             prior login and just needs us to confirm the
//	             subject.
//
//	skip=false → {"challenge": "…", "client_id": "…", …}.  The
//	             SPA renders a password prompt and POSTs back.
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
			writeJSON(w, http.StatusOK, hydraLoginGetResponse{
				Skip:       true,
				RedirectTo: redirect.RedirectTo,
			})
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

// hydraLoginGetResponse is the JSON the SPA reads to decide whether
// to render the form or to skip straight to redirect_to. Exactly one
// of {Skip, Challenge} is meaningful per response.
type hydraLoginGetResponse struct {
	Skip           bool     `json:"skip,omitempty"`
	RedirectTo     string   `json:"redirect_to,omitempty"`
	Challenge      string   `json:"challenge,omitempty"`
	ClientID       string   `json:"client_id,omitempty"`
	ClientName     string   `json:"client_name,omitempty"`
	RequestedScope []string `json:"requested_scope,omitempty"`
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
		// Look up the just-authenticated identity in Kratos so we
		// can pour its traits (email, display_name) into the OIDC
		// id_token Session. Without this, downstream OIDC clients
		// only see a bare `sub` claim and Rivolt's own callback
		// has no email to key off of, so it lands users on a
		// subject-derived UUID that diverges from the cookie /
		// Kratos-session paths.
		var session *hydra.Session
		if d.Kratos != nil && d.Kratos.Enabled() && req.Subject != "" {
			id, err := d.Kratos.GetIdentity(r.Context(), req.Subject)
			if err != nil {
				// Don't fail consent on a Kratos hiccup — the
				// access token still works, the id_token just
				// won't have the rich claims. Log loud so the
				// degradation is visible.
				d.Logger.Warn("hydra consent: kratos lookup",
					"err", err, "subject", req.Subject)
			} else {
				claims := map[string]any{}
				if id.Traits.Email != "" {
					claims["email"] = id.Traits.Email
					claims["email_verified"] = true
				}
				if id.Traits.DisplayName != "" {
					claims["name"] = id.Traits.DisplayName
				}
				// preferred_username must be a stable, short handle —
				// Grafana (and others) keys local user records on it.
				// Use the email local-part so it survives display-name
				// edits and OIDC issuer renames. Falls back to the
				// display name when no email is present.
				if local := emailLocalPart(id.Traits.Email); local != "" {
					claims["preferred_username"] = local
				} else if id.Traits.DisplayName != "" {
					claims["preferred_username"] = id.Traits.DisplayName
				}
				// Project the Kratos role into a `groups` array on
				// the id_token. ArgoCD and Grafana RBAC both key off
				// a list-shaped claim, so even single-role users get
				// a one-element slice. Empty role → omit the claim
				// rather than send `[""]`, which clients would map to
				// an unknown group.
				if g := groupsForRole(id.MetadataPublic.Role); len(g) > 0 {
					claims["groups"] = g
				}
				if len(claims) > 0 {
					// Mirror into AccessToken session as well so
					// Hydra's /userinfo endpoint returns them.
					// Hydra returns *only* what's in Session.IDToken
					// from /userinfo when it was minted from a
					// session that has them; in practice Grafana,
					// ArgoCD and other clients that hit /userinfo
					// for the email/groups payload need them on
					// the access-token session. Send to both —
					// id_token claims show up in the JWT, access
					// token claims surface via /userinfo.
					session = &hydra.Session{
						IDToken:     claims,
						AccessToken: claims,
					}
				}
			}
		}
		redirect, err := d.Hydra.AcceptConsentRequest(r.Context(), challenge,
			hydra.AcceptConsentRequest{
				GrantScope:               req.RequestedScope,
				GrantAccessTokenAudience: req.RequestedAccessTokenAudience,
				Remember:                 true,
				RememberFor:              3600,
				Session:                  session,
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

// groupsForRole maps a Kratos `metadata_public.role` value to the
// list of OIDC `groups` claim entries we emit. Centralised here so
// the mapping is one place to change when downstream clients need
// finer-grained groups (e.g. "rivolt-admin" vs "argocd-admin").
//
// Today: "admin" → ["admins"], anything else → ["users"], empty →
// nil so the claim is omitted entirely.
func groupsForRole(role string) []string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "":
		return nil
	case "admin", "admins":
		return []string{"admins"}
	default:
		return []string{"users"}
	}
}

// emailLocalPart returns the part of `email` before the first '@',
// trimmed and lower-cased. Returns "" for malformed input so callers
// can fall back to a different claim source.
func emailLocalPart(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(email[:at]))
}
