package api

import (
	"context"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/appauth"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/oidc"
)

// AppAuth is the server side of the iPhone app's sign-in (internal/appauth,
// iOS design §2). Mounted under /api/auth/app plus /api/app/config when
// set; never on the admin deployment.
type AppAuth struct {
	Store *appauth.Store
	// NewSession creates a kind=app session for uid and returns its raw
	// token and expiry. (sessions.Store.Create with Kind "app".)
	NewSession func(ctx context.Context, uid uuid.UUID, r *http.Request, deviceLabel string) (string, time.Time, error)
	// CallbackURL is where finish sends the browser: the app's fixed
	// callback (a Universal Link on rivolt.dev, rivolt://auth for a
	// self-hosted server). Never taken from the request (DA, condition 1).
	CallbackURL string
	// SecureCookie marks the state cookie Secure (as the session cookie).
	SecureCookie bool
	// MinIOSBuild, when > 0, is the oldest app build this server accepts;
	// /api/app/config reports it so the app can ask for an update.
	MinIOSBuild int
}

const (
	// appAuthCookie binds finish to the browser that started the sign-in
	// (DA, condition 2). Scoped to /api/auth/app.
	appAuthCookie = "rivolt_app_auth"
	appAuthPath   = "/api/auth/app"
	appFinishPath = appAuthPath + "/finish"
	// AppSessionTTL is an app session's lifetime.
	AppSessionTTL = 90 * 24 * time.Hour
)

// appConfigResponse is GET /api/app/config. The app's AppConfig decodes it.
type appConfigResponse struct {
	Server      string        `json:"server"`
	APIVersion  int           `json:"api_version"`
	AppLogin    bool          `json:"app_login"`
	Push        appConfigPush `json:"push"`
	MinIOSBuild int           `json:"min_ios_build,omitempty"`
}

type appConfigPush struct {
	APNs bool `json:"apns"`
}

// handleAppConfig tells the app whether this is a Rivolt server it can sign
// in to. Unauthenticated; a UX check, not a security boundary.
func handleAppConfig(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := appConfigResponse{Server: "rivolt", APIVersion: 1}
		if d.AppAuth != nil {
			resp.AppLogin = d.OIDC != nil && len(d.OIDC.Providers()) > 0
			resp.MinIOSBuild = d.AppAuth.MinIOSBuild
		}
		w.Header().Set("Cache-Control", "no-cache")
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleAppStart records the app's PKCE challenge, binds this browser with
// the state cookie and sends it into the usual web sign-in, which comes back
// to finish. A browser that's already signed in goes straight to finish.
func (a *AppAuth) handleAppStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || !appauth.ValidChallenge(q.Get("code_challenge")) {
			http.Error(w, "code_challenge (S256) required", http.StatusBadRequest)
			return
		}
		state, err := a.Store.Begin(r.Context(), q.Get("code_challenge"))
		if err != nil {
			http.Error(w, "couldn't start sign-in", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     appAuthCookie,
			Value:    state,
			Path:     appAuthPath,
			MaxAge:   int(appauth.StartTTL / time.Second),
			HttpOnly: true,
			Secure:   a.SecureCookie,
			// Lax: the cookie must come back on the top-level GET from the
			// identity provider's redirect.
			SameSite: http.SameSiteLaxMode,
		})
		if uid, ok := auth.UserFromContext(r.Context()); ok && uid != uuid.Nil && !auth.IsImpersonating(r.Context()) {
			http.Redirect(w, r, appFinishPath, http.StatusFound)
			return
		}
		providers := d.OIDC.Providers()
		sort.Slice(providers, func(i, j int) bool { return providers[i].Name < providers[j].Name })
		switch len(providers) {
		case 0:
			http.Error(w, "sign-in isn't configured on this server", http.StatusServiceUnavailable)
		case 1:
			http.Redirect(w, r, providers[0].StartURL+"?return="+url.QueryEscape(appFinishPath), http.StatusFound)
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_ = appChooseProvider.Execute(w, providerChoices(providers))
		}
	}
}

// handleAppFinish runs after the web sign-in, in the browser that started:
// it mints the one-time code for the signed-in user and sends the browser to
// the app's fixed callback.
func (a *AppAuth) handleAppFinish() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, cookieErr := r.Cookie(appAuthCookie)
		http.SetCookie(w, &http.Cookie{
			Name: appAuthCookie, Value: "", Path: appAuthPath, MaxAge: -1,
			HttpOnly: true, Secure: a.SecureCookie, SameSite: http.SameSiteLaxMode,
		})
		uid, ok := auth.UserFromContext(r.Context())
		if !ok || uid == uuid.Nil || auth.IsImpersonating(r.Context()) {
			a.toCallback(w, r, "error", "access_denied")
			return
		}
		if cookieErr != nil {
			a.toCallback(w, r, "error", "invalid_request")
			return
		}
		code, err := a.Store.Finish(r.Context(), c.Value, uid)
		if err != nil {
			a.toCallback(w, r, "error", "invalid_request")
			return
		}
		a.toCallback(w, r, "code", code)
	}
}

func (a *AppAuth) toCallback(w http.ResponseWriter, r *http.Request, key, value string) {
	u, err := url.Parse(a.CallbackURL)
	if err != nil {
		http.Error(w, "app callback misconfigured", http.StatusInternalServerError)
		return
	}
	q := u.Query()
	q.Set(key, value)
	u.RawQuery = q.Encode()
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, u.String(), http.StatusFound)
}

type appTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	DeviceName   string `json:"device_name"`
}

type appTokenResponse struct {
	Token     string       `json:"token"`
	TokenType string       `json:"token_type"`
	ExpiresAt time.Time    `json:"expires_at"`
	User      appTokenUser `json:"user"`
}

type appTokenUser struct {
	UserID   uuid.UUID `json:"user_id"`
	Username string    `json:"username"`
}

// handleAppToken exchanges the one-time code and the PKCE verifier for an
// app session token. Called by the app directly, not by the browser.
func (a *AppAuth) handleAppToken(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		var req appTokenRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
			return
		}
		uid, err := a.Store.Redeem(r.Context(), req.Code, req.CodeVerifier)
		if errors.Is(err, appauth.ErrInvalid) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		token, expires, err := a.NewSession(r.Context(), uid, r, deviceLabel(req.DeviceName))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
			return
		}
		resp := appTokenResponse{Token: token, TokenType: "Bearer", ExpiresAt: expires.UTC(), User: appTokenUser{UserID: uid}}
		if d.DB != nil {
			_ = d.DB.QueryRowContext(r.Context(), `SELECT username FROM users WHERE id = $1`, uid).Scan(&resp.User.Username)
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// deviceLabel keeps the app's device name printable and short: it's shown
// in the account's sessions list.
func deviceLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if r := []rune(s); len(r) > 64 {
		s = string(r[:64])
	}
	if s == "" {
		return "iPhone app"
	}
	return s
}

type appProviderChoice struct {
	Name string
	Href string
}

func providerChoices(ps []oidc.ProviderListing) []appProviderChoice {
	out := make([]appProviderChoice, 0, len(ps))
	for _, p := range ps {
		out = append(out, appProviderChoice{
			Name: p.DisplayName,
			Href: p.StartURL + "?return=" + url.QueryEscape(appFinishPath),
		})
	}
	return out
}

// appChooseProvider is shown only by a self-hosted server with more than one
// sign-in provider. html/template escapes the names and links.
var appChooseProvider = template.Must(template.New("choose").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in to Rivolt</title>
<style>body{font:17px -apple-system,system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem}a{display:block;padding:.9rem 1rem;margin:.6rem 0;border:1px solid #ccc;border-radius:.6rem;text-decoration:none;color:inherit}</style>
</head><body><h1>Sign in to Rivolt</h1>
{{range .}}<a href="{{.Href}}">Continue with {{.Name}}</a>{{end}}
</body></html>`))
