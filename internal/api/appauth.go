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
	"github.com/apohor/rivolt/internal/sessions"
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
	// AppSessionTTL is an app session's lifetime (sliding; sessions.AppTTL).
	AppSessionTTL = sessions.AppTTL
)

// appConfigResponse is GET /api/app/config. The app's AppConfig decodes it.
type appConfigResponse struct {
	Server      string        `json:"server"`
	APIVersion  int           `json:"api_version"`
	AppLogin    bool          `json:"app_login"`
	Push        appConfigPush `json:"push"`
	MinIOSBuild int           `json:"min_ios_build,omitempty"`
	// IAP is what the app needs to sell Rivolt Pro through StoreKit on this server: absent unless StoreKit is configured here with at least
	// one product id AND billing is on (no bundle id, no products, or billing off, and the app shows no purchase button).
	IAP *appConfigIAP `json:"iap,omitempty"`
}

// appConfigIAP is the StoreKit side of /api/app/config. Environment is "Production" or "Sandbox" (one per deployment); Products are the
// product ids Rivolt Pro is sold as, which are public in the App Store. The server does not say which is monthly or yearly: the app asks
// StoreKit for the products and reads each one's subscription period, so the two cannot drift. A purchase must carry the account's id
// (GET /api/auth/me user_id) as appAccountToken.
type appConfigIAP struct {
	Environment string   `json:"environment"`
	Products    []string `json:"products"`
}

type appConfigPush struct {
	APNs bool `json:"apns"`
	// Live is whether the server sends charging Live Activity updates; the app
	// shows its switch only when it is true.
	Live bool `json:"live"`
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
		// Only a server that is selling: StoreKit products can be configured while RIVOLT_BILLING is off (during setup, after a rollback),
		// and a Subscribe button there would let a user pay Apple for Pro that everyone already has.
		if t := d.AppleIAPTransaction; t != nil && t.Config != nil && len(t.Config.Products) > 0 && d.Entitlements.Billing() {
			ids := make([]string, 0, len(t.Config.Products))
			for id := range t.Config.Products {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			resp.IAP = &appConfigIAP{Environment: t.Config.Environment, Products: ids}
		}
		resp.Push.APNs = d.PushService.APNsEnabled()
		resp.Push.Live = d.PushService.LiveEnabled()
		w.Header().Set("Cache-Control", "no-cache")
		writeJSON(w, http.StatusOK, resp)
	}
}

// handleAppStart records the app's PKCE challenge, binds this browser with
// the state cookie and sends it into the usual web sign-in, which comes back
// to finish. A browser that's already signed in goes straight to finish.
func (a *AppAuth) handleAppStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		q := r.URL.Query()
		if q.Get("code_challenge_method") != "S256" || !appauth.ValidChallenge(q.Get("code_challenge")) {
			http.Error(w, "code_challenge (S256) required", http.StatusBadRequest)
			return
		}
		state, err := a.Store.Begin(r.Context(), q.Get("code_challenge"))
		if errors.Is(err, appauth.ErrBusy) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many sign-ins in progress, try again in a minute", http.StatusServiceUnavailable)
			return
		}
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
		if uid, ok := browserUser(r); ok && uid != uuid.Nil {
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

// handleAppFinishPage runs after the web sign-in, in the browser that
// started. It never mints a code: it asks the signed-in user to confirm
// signing the iPhone app in, so a start link someone else sent can't hand
// them a code (DA on #769). Continue posts the form below to
// handleAppFinish.
func (a *AppAuth) handleAppFinishPage(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, cookieErr := r.Cookie(appAuthCookie)
		uid, ok := browserUser(r)
		if !ok || uid == uuid.Nil {
			a.clearState(w)
			a.toCallback(w, r, "error", "access_denied")
			return
		}
		if cookieErr != nil {
			a.toCallback(w, r, "error", "invalid_request")
			return
		}
		name := ""
		if d.sysDB() != nil {
			_ = d.sysDB().QueryRowContext(r.Context(), `SELECT username FROM users WHERE id = $1`, uid).Scan(&name) // system pool: the app sign-in exchange runs before the app has a session
		}
		consentHeaders(w)
		_ = appConsentPage.Execute(w, appConsent{
			Username: emailOff(name),
			Action:   appFinishPath,
			Token:    a.Store.ConsentToken(c.Value),
		})
	}
}

// handleAppFinish is the consent form's POST: with the state cookie, the
// signed-in user and the form's token, Continue mints the one-time code and
// sends the browser to the app's fixed callback; Cancel tells the app no.
// A cross-site POST carries neither SameSite=Lax cookie, so it's refused.
func (a *AppAuth) handleAppFinish() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		_ = r.ParseForm()
		c, cookieErr := r.Cookie(appAuthCookie)
		if cookieErr != nil || !a.Store.CheckConsent(c.Value, r.PostFormValue("consent")) {
			consentHeaders(w)
			http.Error(w, "This sign-in link has expired or wasn't started on this device. Start again from the Rivolt app.", http.StatusForbidden)
			return
		}
		a.clearState(w)
		uid, ok := browserUser(r)
		if !ok || uid == uuid.Nil || r.PostFormValue("decision") != "allow" {
			a.toCallback(w, r, "error", "access_denied")
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

// browserUser is the signed-in browser user, for the sign-in pages: never an
// impersonated identity and never an app bearer token (DA on #769: the app's
// own token can't be used to mint another one).
func browserUser(r *http.Request) (uuid.UUID, bool) {
	if auth.IsImpersonating(r.Context()) || auth.ViaBearer(r.Context()) {
		return uuid.Nil, false
	}
	return auth.UserFromContext(r.Context())
}

func (a *AppAuth) clearState(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: appAuthCookie, Value: "", Path: appAuthPath, MaxAge: -1,
		HttpOnly: true, Secure: a.SecureCookie, SameSite: http.SameSiteLaxMode,
	})
}

// consentHeaders: never cached, never framed (no clickjacking the Continue
// button), no scripts.
func consentHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
}

type appConsent struct {
	// Username is trusted HTML (emailOff: escaped, then wrapped in markers),
	// safe only as element text. In an attribute, URL or script it needs its
	// own escaping.
	Username template.HTML
	Action   string
	Token    string
}

// emailOff escapes the username and wraps it in Cloudflare's email_off
// markers. Usernames are often email addresses, and Cloudflare's Email
// Address Obfuscation would otherwise replace one with "[email protected]",
// decoded by a script this page's CSP blocks: the user couldn't see which
// account the app is about to use. html/template drops comments written in
// the template itself, so the markers go in as template.HTML around the
// escaped name.
func emailOff(name string) template.HTML {
	return template.HTML("<!--email_off-->" + template.HTMLEscapeString(name) + "<!--/email_off-->")
}

// appConsentPage asks before handing the app a session. html/template
// escapes the token; emailOff escapes the username.
var appConsentPage = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sign in to the Rivolt app</title>
<meta name="color-scheme" content="light dark">
<style>:root{color-scheme:light dark}
body{font:17px -apple-system,system-ui,sans-serif;max-width:28rem;margin:3rem auto;padding:0 1rem;background:#fff;color:#111}
button{display:block;width:100%;font:inherit;padding:.9rem;margin:.6rem 0;border-radius:.6rem;border:1px solid #ccc;background:none;color:#111}
button[value=allow]{background:#0a7d32;color:#fff;border-color:#0a7d32}.note{color:#666;font-size:15px}
@media (prefers-color-scheme:dark){body{background:#111;color:#f2f2f2}button{color:#f2f2f2;border-color:#666}button[value=allow]{background:#2fb466;color:#06210f;border-color:#2fb466}.note{color:#b0b0b0}}</style>
</head><body>
<h1>Sign in to the Rivolt iPhone app?</h1>
<p>The app on this phone will be signed in as <strong>{{.Username}}</strong> and can see your trucks and their history.</p>
<p class="note">Only continue if you just tapped Sign in in the Rivolt app. If a link or message sent you here, cancel.</p>
<form method="post" action="{{.Action}}">
<input type="hidden" name="consent" value="{{.Token}}">
<button type="submit" name="decision" value="allow">Continue</button>
<button type="submit" name="decision" value="deny">Cancel</button>
</form>
</body></html>`))

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
		if d.sysDB() != nil {
			_ = d.sysDB().QueryRowContext(r.Context(), `SELECT username FROM users WHERE id = $1`, uid).Scan(&resp.User.Username) // system pool: the app token exchange, no session yet
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
