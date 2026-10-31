package auth

import (
	"context"
	"github.com/apohor/rivolt/internal/logging"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeUserIDFor is a stand-in for db.UserIDFor. The real function
// uses UUIDv5 under a fixed namespace; for these tests any stable
// mapping is fine as long as it's deterministic.
func fakeUserIDFor(u string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceDNS, []byte(strings.ToLower(strings.TrimSpace(u))))
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// Header sign-in was removed (N48): a user header is never believed, from any peer, whatever the old
// X-Forwarded-* names, so a forged one cannot sign anyone in.
func TestHeader_NeverSignsIn(t *testing.T) {
	s := newTestService(t)
	for _, peer := range []string{"10.0.0.5:54321", "127.0.0.1:9", "192.0.2.7:9"} {
		for _, h := range []string{"X-Forwarded-Preferred-Username", "X-Forwarded-User", "X-Forwarded-Email"} {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = peer
			r.Header.Set(h, "mallory")
			signedIn := false
			s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, signedIn = UserFromContext(r.Context())
			})).ServeHTTP(httptest.NewRecorder(), r)
			if signedIn {
				t.Errorf("%s from %s signed a request in", h, peer)
			}
		}
	}
}

// TestCookie_RoundTrip exercises the issued-cookie path. IssueSession
// plants a cookie; a subsequent request carrying that cookie
// resolves to the same user. Stand-in for what an OIDC callback
// flow does after EnsureUserFull.
func TestCookie_RoundTrip(t *testing.T) {
	s := newTestService(t)

	uid := fakeUserIDFor("alice")
	issueRec := httptest.NewRecorder()
	issueReq := httptest.NewRequest("GET", "/oidc/callback", nil)
	if err := s.IssueSession(issueRec, issueReq, uid); err != nil {
		t.Fatalf("IssueSession: %v", err)
	}
	cookies := issueRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("no cookie issued by IssueSession")
	}

	followup := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		followup.AddCookie(c)
	}
	var got uuid.UUID
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), followup)

	if got != uid {
		t.Fatalf("cookie-resolved user = %s, want %s", got, uid)
	}
}

// TestBypass_InjectsUser covers the debug bypass: when BypassUserID
// is set, an unauthenticated request still resolves to that user.
func TestBypass_InjectsUser(t *testing.T) {
	want := fakeUserIDFor("local")
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		BypassUserID: want,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.7:1234" // not in any trusted CIDR
	var got uuid.UUID
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), r)
	if got != want {
		t.Fatalf("bypass-resolved user = %s, want %s", got, want)
	}
}

// TestKratos_ResolverNotInvokedWithoutCookie pins the cost
// invariant: the resolver must not be called when the inbound
// request has no Kratos cookie. Otherwise every Rivolt-only login
// pays a Whoami round-trip.
func TestKratos_ResolverNotInvokedWithoutCookie(t *testing.T) {
	called := false
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosResolver: func(_ context.Context, _ string) (uuid.UUID, bool, error) {
			called = true
			return uuid.Nil, false, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "1.2.3.4:1"
	rr := httptest.NewRecorder()
	s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, r)
	if called {
		t.Errorf("KratosResolver was invoked despite no Kratos cookie")
	}
}

// TestKratos_ResolverInvokedWithCookie covers the happy path: a
// request bearing ory_kratos_session is resolved through the
// resolver and the returned UUID lands in the request context.
func TestKratos_ResolverInvokedWithCookie(t *testing.T) {
	want := fakeUserIDFor("kratos@example.com")
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosResolver: func(_ context.Context, cookieHeader string) (uuid.UUID, bool, error) {
			if !strings.Contains(cookieHeader, "ory_kratos_session=abc") {
				t.Errorf("resolver got cookieHeader = %q", cookieHeader)
			}
			return want, true, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "abc"})
	r.RemoteAddr = "1.2.3.4:1"

	var got uuid.UUID
	s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), r)

	if got != want {
		t.Errorf("user = %s, want %s", got, want)
	}
}

// TestKratos_ResolverFallsBackToCookieOnNoSession asserts that a
// stale Kratos cookie (resolver says ok=false) doesn't lock the
// user out — the cookie path still runs after.
func TestKratos_ResolverFallsBackToCookieOnNoSession(t *testing.T) {
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			return uuid.Nil, false, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Forge a Rivolt cookie via the HMAC encode path, which is
	// the same value Login would set on a real flow.
	uid := fakeUserIDFor("u")
	tok, err := s.encode(token{UserID: uid, ExpiresAt: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "stale"})
	r.AddCookie(&http.Cookie{Name: "rivolt_session", Value: tok})
	r.RemoteAddr = "1.2.3.4:1"

	var got uuid.UUID
	s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(httptest.NewRecorder(), r)

	if got != uid {
		t.Errorf("expected fallback to cookie issuer, got %s", got)
	}
}

// TestWithImpersonation_SwapsIdentityAndMarksContext covers the
// core contract impersonationMW relies on: after WithImpersonation,
// UserFromContext must return the target (so every store/RLS lookup
// downstream sees the target's data), while ImpersonatorFromContext
// recovers the real admin id for audit purposes.
func TestWithImpersonation_SwapsIdentityAndMarksContext(t *testing.T) {
	admin := uuid.New()
	target := uuid.New()
	ctx := WithImpersonation(context.Background(), admin, target)

	if uid, ok := UserFromContext(ctx); !ok || uid != target {
		t.Errorf("UserFromContext = %s, %v; want %s, true", uid, ok, target)
	}
	if got, ok := ImpersonatorFromContext(ctx); !ok || got != admin {
		t.Errorf("ImpersonatorFromContext = %s, %v; want %s, true", got, ok, admin)
	}
	if !IsImpersonating(ctx) {
		t.Error("IsImpersonating = false, want true")
	}
}

// TestIsImpersonating_FalseOnPlainContext ensures a normal
// (non-impersonated) authenticated context — the overwhelming
// majority of requests — never trips the impersonation guard.
func TestIsImpersonating_FalseOnPlainContext(t *testing.T) {
	ctx := WithUser(context.Background(), uuid.New())
	if IsImpersonating(ctx) {
		t.Error("IsImpersonating = true on a plain WithUser context")
	}
	if _, ok := ImpersonatorFromContext(ctx); ok {
		t.Error("ImpersonatorFromContext ok=true on a plain WithUser context")
	}
}

// A valid IdP session whose identity has no account on a closed
// server stays unauthenticated but names the reason, so the SPA can
// show it instead of a bare sign-in page.
func TestKratos_ErrSignupClosedSetsAuthErrorHeader(t *testing.T) {
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			return uuid.Nil, false, ErrSignupClosed
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "valid-at-kratos"})
	r.RemoteAddr = "1.2.3.4:1"
	rec := httptest.NewRecorder()
	var authed bool
	s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, authed = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, r)
	if authed {
		t.Fatal("must stay unauthenticated")
	}
	if got := rec.Header().Get(AuthErrorHeader); got != "signups_disabled" {
		t.Fatalf("%s = %q, want signups_disabled", AuthErrorHeader, got)
	}
}

// An ordinary "no session" answer sets no header.
func TestKratos_NoSessionSetsNoAuthErrorHeader(t *testing.T) {
	s, _ := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			return uuid.Nil, false, nil
		},
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "stale"})
	rec := httptest.NewRecorder()
	s.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(rec, r)
	if got := rec.Header().Get(AuthErrorHeader); got != "" {
		t.Fatalf("header = %q, want none", got)
	}
}

// A Kratos session cookie scoped to a parent domain reaches sibling hosts
// (preview, demo). With KratosHost set, only requests for that host may
// authenticate with it; the others never even call the resolver.
func TestKratos_HostGuard(t *testing.T) {
	want := fakeUserIDFor("kratos@example.com")
	calls := 0
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosHost:   "https://Rivolt.dev/",
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			calls++
			return want, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	try := func(host string) (uuid.UUID, bool) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "abc"})
		r.RemoteAddr = "1.2.3.4:1"
		var got uuid.UUID
		s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, _ = UserFromContext(r.Context())
		})).ServeHTTP(httptest.NewRecorder(), r)
		return got, s.HasCredentials(r)
	}
	for _, h := range []string{"rivolt.dev", "RIVOLT.DEV", "rivolt.dev:443", "rivolt.dev."} {
		if got, has := try(h); got != want || !has {
			t.Errorf("host %q: user %s credentials %v, want the Kratos user", h, got, has)
		}
	}
	before := calls
	for _, h := range []string{"preview.rivolt.dev", "demo.rivolt.dev", "evil.example", "rivolt.dev.evil.example", ""} {
		if got, has := try(h); got != uuid.Nil || has {
			t.Errorf("host %q authenticated with the Kratos cookie (user %s, credentials %v)", h, got, has)
		}
	}
	if calls != before {
		t.Errorf("resolver called %d times for foreign hosts", calls-before)
	}
}

// An install that is NOT the cookie host (preview) is configured with the
// same cookie host as prod; its own Host never matches, so a prod cookie
// that reaches it through a parent-domain Domain is refused.
func TestKratos_HostGuard_PreviewConfiguredWithTheCookieHost(t *testing.T) {
	calls := 0
	s, err := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosHost:   "rivolt.dev", // RIVOLT_KRATOS_COOKIE_HOST, not preview's own base URL
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			calls++
			return fakeUserIDFor("prod-user@example.com"), true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "preview.rivolt.dev"
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "prod-cookie"})
	r.RemoteAddr = "1.2.3.4:1"
	var got uuid.UUID
	s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), r)
	if got != uuid.Nil || calls != 0 || s.HasCredentials(r) {
		t.Errorf("preview accepted a prod Kratos cookie: user %s, resolver calls %d", got, calls)
	}
}

// Without KratosHost the issuer works for any host, as before.
func TestKratos_NoHostGuardByDefault(t *testing.T) {
	want := fakeUserIDFor("kratos@example.com")
	s, _ := New(Config{
		CookieSecret: []byte("0123456789abcdef0123456789abcdef"),
		UserIDFor:    fakeUserIDFor,
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) {
			return want, true, nil
		},
	})
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "anything.example"
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "abc"})
	var got uuid.UUID
	s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
	})).ServeHTTP(httptest.NewRecorder(), r)
	if got != want {
		t.Errorf("user = %s without a configured host, want %s", got, want)
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"rivolt.dev": "rivolt.dev", "Rivolt.Dev:8443": "rivolt.dev", "https://rivolt.dev/x?y": "rivolt.dev",
		"http://localhost:8080": "localhost", "rivolt.dev.": "rivolt.dev", "[::1]:80": "::1", "": "",
	} {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

// Every way in is tagged for the access log and counted for the metric:
// kratos, cookie and bypass; an unauthenticated request is neither.
func TestAuthMethodIsRecordedForEveryPath(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	var counted []string
	count := func(m string) { counted = append(counted, m) }

	method := func(s *Service, r *http.Request) (string, bool) {
		var m string
		var ok bool
		s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, ok = UserFromContext(r.Context())
			m = logging.AuthMethodFromContext(r.Context())
		})).ServeHTTP(httptest.NewRecorder(), r)
		return m, ok
	}

	// kratos
	ks, _ := New(Config{CookieSecret: key, UserIDFor: fakeUserIDFor, OnAuth: count,
		KratosResolver: func(context.Context, string) (uuid.UUID, bool, error) { return fakeUserIDFor("k"), true, nil }})
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: "ory_kratos_session", Value: "abc"})
	if m, ok := method(ks, r); !ok || m != "kratos" {
		t.Errorf("kratos: %q %v", m, ok)
	}

	// cookie
	cs, _ := New(Config{CookieSecret: key, UserIDFor: fakeUserIDFor, OnAuth: count})
	rec := httptest.NewRecorder()
	if err := cs.IssueSession(rec, httptest.NewRequest("GET", "/", nil), fakeUserIDFor("c")); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/", nil)
	for _, c := range rec.Result().Cookies() {
		r.AddCookie(c)
	}
	if m, ok := method(cs, r); !ok || m != "cookie" {
		t.Errorf("cookie: %q %v", m, ok)
	}

	// bypass
	bs, _ := New(Config{CookieSecret: key, UserIDFor: fakeUserIDFor, BypassUserID: fakeUserIDFor("b"), OnAuth: count})
	if m, ok := method(bs, httptest.NewRequest("GET", "/", nil)); !ok || m != "bypass" {
		t.Errorf("bypass: %q %v", m, ok)
	}

	// nobody: no method, nothing counted
	before := len(counted)
	if m, ok := method(cs, httptest.NewRequest("GET", "/", nil)); ok || m != "" {
		t.Errorf("anonymous: %q %v", m, ok)
	}
	if len(counted) != before {
		t.Error("an anonymous request was counted as authenticated")
	}
	if got := strings.Join(counted, ","); got != "kratos,cookie,bypass" {
		t.Errorf("metric hook saw %q", got)
	}
}
