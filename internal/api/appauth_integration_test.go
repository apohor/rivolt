//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	finish := func(r *http.Request) *url.URL {
		t.Helper()
		w := httptest.NewRecorder()
		a.handleAppFinish()(w, r)
		if w.Code != http.StatusFound {
			t.Fatalf("finish: %d", w.Code)
		}
		u, err := url.Parse(w.Header().Get("Location"))
		if err != nil || u.Scheme != "rivolt" || u.Host != "auth" {
			t.Fatalf("finish went to %q, not the fixed callback", w.Header().Get("Location"))
		}
		return u
	}
	token := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.handleAppToken(d)(w, httptest.NewRequest(http.MethodPost, "/api/auth/app/token", strings.NewReader(body)))
		return w
	}

	t.Run("start, finish, token", func(t *testing.T) {
		w := start("code_challenge_method=S256&code_challenge=" + challenge)
		if w.Code != http.StatusFound || w.Header().Get("Location") != appFinishPath {
			t.Fatalf("signed-in start: %d %q", w.Code, w.Header().Get("Location"))
		}
		var state *http.Cookie
		for _, c := range w.Result().Cookies() {
			if c.Name == appAuthCookie {
				state = c
			}
		}
		if state == nil || !state.HttpOnly || state.Path != appAuthPath || state.SameSite != http.SameSiteLaxMode {
			t.Fatalf("state cookie: %+v", state)
		}
		r := signedIn(httptest.NewRequest(http.MethodGet, appFinishPath, nil))
		r.AddCookie(state)
		code := finish(r).Query().Get("code")
		if code == "" {
			t.Fatal("no code")
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

	t.Run("finish refuses without the browser that started, or without a user", func(t *testing.T) {
		if got := finish(signedIn(httptest.NewRequest(http.MethodGet, appFinishPath, nil))).Query().Get("error"); got != "invalid_request" {
			t.Fatalf("no state cookie: %q", got)
		}
		w := start("code_challenge_method=S256&code_challenge=" + challenge)
		state := w.Result().Cookies()[0]
		anon := httptest.NewRequest(http.MethodGet, appFinishPath, nil)
		anon.AddCookie(state)
		if got := finish(anon).Query().Get("error"); got != "access_denied" {
			t.Fatalf("not signed in: %q", got)
		}
		viewAs := httptest.NewRequest(http.MethodGet, appFinishPath, nil)
		viewAs = viewAs.WithContext(auth.WithImpersonation(viewAs.Context(), uuid.New(), uid))
		viewAs.AddCookie(state)
		if got := finish(viewAs).Query().Get("error"); got != "access_denied" {
			t.Fatalf("impersonating: %q", got)
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
