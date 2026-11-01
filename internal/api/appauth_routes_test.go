package api

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/applebill"
	"github.com/apohor/rivolt/internal/appleid"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
)

// App sign-in exists only when main wired it (a callback configured, OIDC on,
// not the admin deployment): otherwise its routes aren't there at all, so no
// sign-in rows can be created while app login is off (DA on #769).
func TestAppSignInNotMountedUnlessConfigured(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	for name, d := range map[string]Deps{
		"no app auth":           {Auth: svc},
		"app auth without OIDC": {Auth: svc, AppAuth: &AppAuth{CallbackURL: "rivolt://auth"}},
	} {
		t.Run(name, func(t *testing.T) {
			r := New(d)
			for _, req := range []*http.Request{
				httptest.NewRequest(http.MethodGet, "/api/auth/app/start?code_challenge_method=S256&code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM", nil),
				httptest.NewRequest(http.MethodGet, "/api/auth/app/finish", nil),
				httptest.NewRequest(http.MethodPost, "/api/auth/app/finish", nil),
				httptest.NewRequest(http.MethodPost, "/api/auth/app/token", strings.NewReader(`{}`)),
			} {
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
					t.Errorf("%s %s: %d, want not mounted", req.Method, req.URL.Path, w.Code)
				}
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/app/config", nil))
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"app_login":false`) {
				t.Fatalf("config: %d %s", w.Code, w.Body)
			}
		})
	}
}

// Apple's notification endpoint is public (Apple sends no cookie), only mounted when main wired it, capped at 16 KB
// and rate limited per address; tested through the real router with the full URL.
func TestAppleNotificationsRoute(t *testing.T) {
	svc, err := auth.New(auth.Config{UserIDFor: db.UserIDFor})
	if err != nil {
		t.Fatal(err)
	}
	post := func(r http.Handler, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/apple/notifications", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "198.51.100.7:4444"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if c := post(New(Deps{Auth: svc}), "payload=x"); c != http.StatusNotFound && c != http.StatusMethodNotAllowed {
		t.Errorf("unwired: %d, want not mounted", c)
	}
	var gotLen int
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "too big", http.StatusRequestEntityTooLarge)
			return
		}
		gotLen = len(b)
	})
	r := New(Deps{Auth: svc, AppleNotifications: h})
	if c := post(r, "payload=abc"); c != http.StatusOK || gotLen != len("payload=abc") {
		t.Errorf("wired, no cookie: %d (read %d bytes)", c, gotLen)
	}
	if c := post(r, "payload="+strings.Repeat("a", appleid.MaxNotificationBody)); c != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: %d, want 413", c)
	}
	limited := 0
	for i := 0; i < 40; i++ {
		if post(r, "payload=abc") == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Error("40 quick requests from one address were never rate limited")
	}
}

// /api/app/config carries the StoreKit products only when the server is selling (billing on) and StoreKit has at least one product: with
// billing off a Subscribe button would let a user pay Apple for Pro that everyone already has, and the app shows no purchase button when
// the field is absent.
func TestAppConfigCarriesTheStoreKitProducts(t *testing.T) {
	get := func(d Deps) string {
		w := httptest.NewRecorder()
		handleAppConfig(d)(w, httptest.NewRequest(http.MethodGet, "/api/app/config", nil))
		return w.Body.String()
	}
	cfg := func(products map[string]bool) *applebill.Transaction {
		return &applebill.Transaction{Config: &applebill.Config{BundleID: "dev.rivolt.test", Environment: applebill.EnvSandbox, Products: products}}
	}
	both := map[string]bool{"test.pro.year": true, "test.pro.month": true}
	billingOn := entitlements.New(new(sql.DB), entitlements.Config{Billing: true}) // never queried here: only Billing() is read
	billingOff := entitlements.New(nil, entitlements.Config{})
	with := get(Deps{Entitlements: billingOn, AppleIAPTransaction: cfg(both)})
	if !strings.Contains(with, `"iap":{"environment":"Sandbox","products":["test.pro.month","test.pro.year"]}`) {
		t.Errorf("billing on, StoreKit configured: %s", with)
	}
	for name, d := range map[string]Deps{
		"no StoreKit":                        {Entitlements: billingOn},
		"no config":                          {Entitlements: billingOn, AppleIAPTransaction: &applebill.Transaction{}},
		"billing on, no product ids":         {Entitlements: billingOn, AppleIAPTransaction: cfg(nil)},
		"billing OFF, products configured":   {Entitlements: billingOff, AppleIAPTransaction: cfg(both)},
		"no entitlement service, configured": {AppleIAPTransaction: cfg(both)},
	} {
		if body := get(d); strings.Contains(body, `"iap"`) {
			t.Errorf("%s: iap must be absent: %s", name, body)
		}
	}
}
