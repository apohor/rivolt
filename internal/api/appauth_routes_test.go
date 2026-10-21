package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
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

// The consent page keeps the account name out of Cloudflare's email
// obfuscation (which would show "[email protected]" under this page's CSP) and
// still escapes it.
func TestConsentPageShieldsTheNameFromEmailObfuscation(t *testing.T) {
	var b strings.Builder
	err := appConsentPage.Execute(&b, appConsentView{
		appConsent: appConsent{Username: `sam<script>@example.com`, Action: appFinishPath, Token: "tok"},
		Off:        cfEmailOff, On: cfEmailOn,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := b.String()
	if !strings.Contains(got, "<strong><!--email_off-->sam&lt;script&gt;@example.com<!--/email_off--></strong>") {
		t.Errorf("name is not wrapped and escaped: %s", got)
	}
}
