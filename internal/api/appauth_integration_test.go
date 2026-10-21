//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/appauth"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/testpg"
)

// The app's sign-in end to end, with the web sign-in in the middle stood in
// for by a request context that already carries the user (what the auth
// middleware does for a signed-in browser).
func TestAppSignInFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "app-flow")
	if err != nil {
		t.Fatal(err)
	}
	pepper := []byte("0123456789abcdef0123456789abcdef")
	store, err := appauth.New(pool, pepper)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := sessions.New(pool, pepper)
	if err != nil {
		t.Fatal(err)
	}
	a := &AppAuth{
		Store:       store,
		CallbackURL: "rivolt://auth",
		NewSession: func(ctx context.Context, uid uuid.UUID, r *http.Request, label string) (string, time.Time, error) {
			s, raw, err := ss.Create(ctx, uid, AppSessionTTL, sessions.CreateOpts{DeviceLabel: label, Kind: sessions.KindApp})
			return raw, s.ExpiresAt, err
		},
	}
	d := Deps{DB: pool, AppAuth: a}
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := appauth.Challenge(verifier)
	signedIn := func(r *http.Request) *http.Request { return r.WithContext(auth.WithUser(r.Context(), uid)) }

	start := func(q string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleAppStart(d)(w, signedIn(httptest.NewRequest(http.MethodGet, "/api/auth/app/start?"+q, nil)))
		return w
	}
	// toCallback reads a redirect to the app's fixed callback.
	toCallback := func(w *httptest.ResponseRecorder) *url.URL {
		t.Helper()
		if w.Code != http.StatusFound {
			t.Fatalf("expected a redirect to the app, got %d: %s", w.Code, w.Body)
		}
		u, err := url.Parse(w.Header().Get("Location"))
		if err != nil || u.Scheme != "rivolt" || u.Host != "auth" {
			t.Fatalf("redirect went to %q, not the fixed callback", w.Header().Get("Location"))
		}
		return u
	}
	// page is GET finish: the consent page, never a code.
	page := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleAppFinishPage(d)(w, r)
		return w
	}
	consentRE := regexp.MustCompile(`name="consent" value="([^"]+)"`)
	// post is the consent form's POST.
	post := func(form url.Values, user bool, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, appFinishPath, strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if user {
			r = signedIn(r)
		}
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		a.handleAppFinish()(w, r)
		return w
	}
	stateCookie := func(w *httptest.ResponseRecorder) *http.Cookie {
		t.Helper()
		for _, c := range w.Result().Cookies() {
			if c.Name == appAuthCookie {
				return c
			}
		}
		t.Fatal("no state cookie")
		return nil
	}
	token := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleAppToken(d)(w, httptest.NewRequest(http.MethodPost, "/api/auth/app/token", strings.NewReader(body)))
		return w
	}

	t.Run("start, consent, token", func(t *testing.T) {
		w := start("code_challenge_method=S256&code_challenge=" + challenge)
		if w.Code != http.StatusFound || w.Header().Get("Location") != appFinishPath {
			t.Fatalf("signed-in start: %d %q", w.Code, w.Header().Get("Location"))
		}
		state := stateCookie(w)
		if !state.HttpOnly || state.Path != appAuthPath || state.SameSite != http.SameSiteLaxMode {
			t.Fatalf("state cookie: %+v", state)
		}

		r := signedIn(httptest.NewRequest(http.MethodGet, appFinishPath, nil))
		r.AddCookie(state)
		w = page(r)
		if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
			t.Fatalf("GET finish must show the consent page, got %d to %q", w.Code, w.Header().Get("Location"))
		}
		if !strings.Contains(w.Body.String(), "app-flow") || w.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("consent page: %s", w.Body)
		}
		m := consentRE.FindStringSubmatch(w.Body.String())
		if m == nil {
			t.Fatal("no consent token on the page")
		}
		consent := m[1]

		if w := post(url.Values{"decision": {"allow"}}, true, state); w.Code != http.StatusForbidden {
			t.Fatalf("POST without the consent token: %d", w.Code)
		}
		if w := post(url.Values{"decision": {"allow"}, "consent": {consent}}, true, nil); w.Code != http.StatusForbidden {
			t.Fatalf("POST without the state cookie (cross-site): %d", w.Code)
		}
		code := toCallback(post(url.Values{"decision": {"allow"}, "consent": {consent}}, true, state)).Query().Get("code")
		if code == "" {
			t.Fatal("no code after consent")
		}

		w = token(`{"code":"` + code + `","code_verifier":"` + verifier + `","device_name":"Test iPhone\n"}`)
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("token: %d %s", w.Code, w.Body)
		}
		var resp appTokenResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Token == "" || resp.TokenType != "Bearer" || resp.User.UserID != uid || resp.User.Username != "app-flow" {
			t.Fatalf("token response %+v", resp)
		}
		if d := time.Until(resp.ExpiresAt); d < AppSessionTTL-time.Minute || d > AppSessionTTL+time.Minute {
			t.Fatalf("expires in %v", d)
		}
		var kind, label string
		if err := pool.QueryRowContext(ctx,
			`SELECT kind, device_label FROM sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 1`, uid).Scan(&kind, &label); err != nil {
			t.Fatal(err)
		}
		if kind != sessions.KindApp || label != "Test iPhone" {
			t.Fatalf("session kind %q label %q", kind, label)
		}
		if w := token(`{"code":"` + code + `","code_verifier":"` + verifier + `"}`); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid_grant") {
			t.Fatalf("second redeem: %d %s", w.Code, w.Body)
		}
	})

	t.Run("cancel, and refusals", func(t *testing.T) {
		state := stateCookie(start("code_challenge_method=S256&code_challenge=" + challenge))
		consent := a.Store.ConsentToken(state.Value)
		if got := toCallback(post(url.Values{"decision": {"deny"}, "consent": {consent}}, true, state)).Query().Get("error"); got != "access_denied" {
			t.Fatalf("cancel: %q", got)
		}

		if got := toCallback(page(signedIn(httptest.NewRequest(http.MethodGet, appFinishPath, nil)))).Query().Get("error"); got != "invalid_request" {
			t.Fatalf("GET finish without the state cookie: %q", got)
		}
		state = stateCookie(start("code_challenge_method=S256&code_challenge=" + challenge))
		anon := httptest.NewRequest(http.MethodGet, appFinishPath, nil)
		anon.AddCookie(state)
		if got := toCallback(page(anon)).Query().Get("error"); got != "access_denied" {
			t.Fatalf("not signed in: %q", got)
		}
		viewAs := httptest.NewRequest(http.MethodGet, appFinishPath, nil)
		viewAs = viewAs.WithContext(auth.WithImpersonation(viewAs.Context(), uuid.New(), uid))
		viewAs.AddCookie(state)
		if got := toCallback(page(viewAs)).Query().Get("error"); got != "access_denied" {
			t.Fatalf("impersonating: %q", got)
		}
		if got := toCallback(post(url.Values{"decision": {"allow"}, "consent": {a.Store.ConsentToken(state.Value)}}, false, state)).Query().Get("error"); got != "access_denied" {
			t.Fatalf("POST not signed in: %q", got)
		}
	})

	t.Run("cookie and bearer kinds stay apart", func(t *testing.T) {
		_, webTok, err := ss.Create(ctx, uid, time.Hour, sessions.CreateOpts{})
		if err != nil {
			t.Fatal(err)
		}
		_, appTok, err := ss.Create(ctx, uid, time.Hour, sessions.CreateOpts{Kind: sessions.KindApp})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ss.Lookup(ctx, webTok, sessions.KindWeb); err != nil {
			t.Fatalf("web token as web: %v", err)
		}
		if _, err := ss.Lookup(ctx, appTok, sessions.KindApp); err != nil {
			t.Fatalf("app token as app: %v", err)
		}
		if _, err := ss.Lookup(ctx, appTok, sessions.KindWeb); err == nil {
			t.Fatal("an app token worked as a browser cookie")
		}
		if _, err := ss.Lookup(ctx, webTok, sessions.KindApp); err == nil {
			t.Fatal("a browser cookie worked as an app token")
		}
		if _, err := sessions.NewAuthAdapter(ss).Lookup(ctx, appTok); err == nil {
			t.Fatal("the cookie adapter accepted an app token")
		}
	})

	t.Run("start wants an S256 challenge", func(t *testing.T) {
		for _, q := range []string{"", "code_challenge=" + challenge, "code_challenge_method=plain&code_challenge=" + challenge, "code_challenge_method=S256&code_challenge=short"} {
			if w := start(q); w.Code != http.StatusBadRequest {
				t.Errorf("%q: %d", q, w.Code)
			}
		}
	})

	t.Run("config", func(t *testing.T) {
		w := httptest.NewRecorder()
		handleAppConfig(Deps{})(w, httptest.NewRequest(http.MethodGet, "/api/app/config", nil))
		if !strings.Contains(w.Body.String(), `"server":"rivolt"`) || !strings.Contains(w.Body.String(), `"app_login":false`) {
			t.Fatalf("config without app sign-in: %s", w.Body)
		}
	})
}
