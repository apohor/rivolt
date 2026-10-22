//go:build integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/appauth"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/oidc"
	"github.com/apohor/rivolt/internal/sessions"
	"github.com/apohor/rivolt/internal/testpg"
)

// lockedBuffer collects everything logged during the test.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// The iPhone app's whole path through the real router: a browser session
// signs the app in (start, consent, token), the token works as a bearer and
// nowhere else, and neither the one-time code nor any token reaches the logs
// (DA on #769).
func TestAppBearerThroughTheRouter(t *testing.T) {
	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "bearer-flow")
	if err != nil {
		t.Fatal(err)
	}
	pepper := []byte("0123456789abcdef0123456789abcdef")
	ss, err := sessions.New(pool, pepper)
	if err != nil {
		t.Fatal(err)
	}
	authSvc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	authSvc.WithSessionStore(sessions.NewAuthAdapter(ss)).AcceptBearer(true)
	oidcSvc, err := oidc.New(ctx, oidc.Config{
		IssueSession: authSvc.IssueSession,
		EnsureUser: func(context.Context, string, string, string, bool) (uuid.UUID, error) {
			return uid, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := appauth.New(pool, pepper)
	if err != nil {
		t.Fatal(err)
	}
	router := New(Deps{
		Auth: authSvc,
		DB:   pool,
		OIDC: oidcSvc,
		AppAuth: &AppAuth{
			Store:       store,
			CallbackURL: "rivolt://auth",
			NewSession: func(ctx context.Context, uid uuid.UUID, r *http.Request, label string) (string, time.Time, error) {
				s, raw, err := ss.Create(ctx, uid, AppSessionTTL, sessions.CreateOpts{DeviceLabel: label, Kind: sessions.KindApp})
				return raw, s.ExpiresAt, err
			},
		},
	})
	do := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}

	// The browser: a signed-in web session.
	_, webTok, err := ss.Create(ctx, uid, time.Hour, sessions.CreateOpts{})
	if err != nil {
		t.Fatal(err)
	}
	browser := func(method, target string, body string) *http.Request {
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: webTok})
		if body != "" {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return r
	}

	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	w := do(browser(http.MethodGet, "/api/auth/app/start?code_challenge_method=S256&code_challenge="+appauth.Challenge(verifier), ""))
	if w.Code != http.StatusFound {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	var state *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == appAuthCookie {
			state = c
		}
	}
	if state == nil {
		t.Fatal("no state cookie")
	}
	page := browser(http.MethodGet, appFinishPath, "")
	page.AddCookie(state)
	w = do(page)
	m := regexp.MustCompile(`name="consent" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if w.Code != http.StatusOK || m == nil {
		t.Fatalf("consent page: %d", w.Code)
	}
	post := browser(http.MethodPost, appFinishPath, url.Values{"consent": {m[1]}, "decision": {"allow"}}.Encode())
	post.AddCookie(state)
	w = do(post)
	loc, _ := url.Parse(w.Header().Get("Location"))
	code := loc.Query().Get("code")
	if w.Code != http.StatusFound || code == "" {
		t.Fatalf("consent post: %d %q", w.Code, w.Header().Get("Location"))
	}
	w = do(httptest.NewRequest(http.MethodPost, "/api/auth/app/token",
		strings.NewReader(`{"code":"`+code+`","code_verifier":"`+verifier+`","device_name":"Test iPhone"}`)))
	var tok appTokenResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &tok) != nil || tok.Token == "" {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}

	app := func(method, target, bearer string) *http.Request {
		r := httptest.NewRequest(method, target, nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		return r
	}

	t.Run("the token works as a bearer", func(t *testing.T) {
		if w := do(app(http.MethodGet, "/api/auth/me", tok.Token)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "bearer-flow") {
			t.Fatalf("me via bearer: %d %s", w.Code, w.Body)
		}
	})

	t.Run("and nowhere else", func(t *testing.T) {
		if w := do(app(http.MethodGet, "/api/auth/me", webTok)); w.Code != http.StatusUnauthorized {
			t.Fatalf("browser cookie value as bearer: %d", w.Code)
		}
		asCookie := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
		asCookie.AddCookie(&http.Cookie{Name: "rivolt_session", Value: tok.Token})
		if w := do(asCookie); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "bearer-flow") {
			t.Fatal("the app token worked as a browser cookie")
		}
		// The sign-in pages don't take the app's own token as a signed-in
		// browser: start sends it to the provider list, not to finish.
		start := app(http.MethodGet, "/api/auth/app/start?code_challenge_method=S256&code_challenge="+appauth.Challenge(verifier), tok.Token)
		if w := do(start); w.Header().Get("Location") == appFinishPath {
			t.Fatal("start treated the app token as a signed-in browser")
		}
	})

	t.Run("app sessions slide", func(t *testing.T) {
		if _, err := pool.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = now() - interval '2 days', expires_at = now() + interval '1 day' WHERE kind = 'app' AND user_id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		if w := do(app(http.MethodGet, "/api/auth/me", tok.Token)); w.Code != http.StatusOK {
			t.Fatalf("me: %d", w.Code)
		}
		var left time.Duration
		var expires time.Time
		if err := pool.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE kind = 'app' AND user_id = $1`, uid).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		left = time.Until(expires)
		if left < AppSessionTTL-time.Hour {
			t.Fatalf("expiry didn't slide: %v left", left)
		}
	})

	t.Run("writes: only the app's own", func(t *testing.T) {
		// A token from the real sign-in is an app session, so the allowlist applies to it.
		for _, c := range [][2]string{
			{http.MethodPost, "/api/settings/charging"},
			{http.MethodPut, "/api/settings/charging"},
			{http.MethodDelete, "/api/charges/chg-x"},
			{http.MethodPost, "/api/rivian/logout"},
			{http.MethodDelete, "/api/auth/sessions/" + uuid.NewString()},
			{http.MethodPost, "/api/push/subscribe"},
		} {
			w := do(app(c[0], c[1], tok.Token))
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), appWriteDenied) {
				t.Errorf("%s %s with the app token: %d %q, want the app-write refusal", c[0], c[1], w.Code, w.Body.String())
			}
			// The browser session is not held to the list.
			if w := do(browser(c[0], c[1], "")); strings.Contains(w.Body.String(), appWriteDenied) {
				t.Errorf("%s %s refused for the browser session", c[0], c[1])
			}
		}
		// The reads that are not plain reads: refused for the app's token, open to the browser.
		for _, p := range []string{"/api/export", "/api/data/backup", "/api/state/veh-x/fresh"} {
			if w := do(app(http.MethodGet, p, tok.Token)); w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), appWriteDenied) {
				t.Errorf("GET %s with the app token: %d %q, want the refusal", p, w.Code, w.Body.String())
			}
			if w := do(browser(http.MethodGet, p, "")); strings.Contains(w.Body.String(), appWriteDenied) {
				t.Errorf("GET %s refused for the browser session", p)
			}
		}
		if w := do(app(http.MethodGet, "/api/ledger/monthly.csv", tok.Token)); strings.Contains(w.Body.String(), appWriteDenied) {
			t.Error("the Money tab's CSV was refused for the app token")
		}
		for _, c := range [][2]string{
			{http.MethodPost, "/api/push/apns/test"},
			{http.MethodPost, "/api/push/apns/register"},
			{http.MethodPost, "/api/milestones/dismiss"},
		} {
			if w := do(app(c[0], c[1], tok.Token)); strings.Contains(w.Body.String(), appWriteDenied) {
				t.Errorf("%s %s refused for the app token, but the app does this", c[0], c[1])
			}
		}
	})

	t.Run("sign out ends the token", func(t *testing.T) {
		if w := do(app(http.MethodPost, "/api/auth/logout", tok.Token)); w.Code != http.StatusNoContent {
			t.Fatalf("logout: %d", w.Code)
		}
		if w := do(app(http.MethodGet, "/api/auth/me", tok.Token)); w.Code != http.StatusUnauthorized {
			t.Fatalf("token after logout: %d", w.Code)
		}
		if w := do(browser(http.MethodGet, "/api/auth/me", "")); w.Code != http.StatusOK {
			t.Fatalf("the browser session went with it: %d", w.Code)
		}
	})

	t.Run("nothing secret in the logs", func(t *testing.T) {
		out := logs.String()
		if !strings.Contains(out, `"auth_method":"bearer"`) {
			t.Fatalf("the access log didn't run (no bearer line): %.400s", out)
		}
		for name, secret := range map[string]string{"code": code, "app token": tok.Token, "web token": webTok, "state": state.Value, "consent": m[1]} {
			if strings.Contains(out, secret) {
				t.Errorf("the %s appears in the logs", name)
			}
		}
	})
}
