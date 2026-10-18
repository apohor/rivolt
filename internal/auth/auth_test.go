package auth

import (
	"context"
	"github.com/apohor/rivolt/internal/clientip"
	"net"
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

func newTestService(t *testing.T, cidrs []string) *Service {
	t.Helper()
	nets, err := ParseTrustedCIDRs(strings.Join(cidrs, ","))
	if err != nil {
		t.Fatalf("ParseTrustedCIDRs: %v", err)
	}
	s, err := New(Config{
		CookieSecret:      []byte("0123456789abcdef0123456789abcdef"),
		TrustedProxyCIDRs: nets,
		UserIDFor:         fakeUserIDFor,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

// TestHeader_IgnoredWhenCIDRListEmpty covers the empty-list default.
// No trusted CIDRs means a client pretending to be oauth2-proxy
// must be ignored completely — otherwise anyone can send a header
// and become anyone.
func TestHeader_IgnoredWhenCIDRListEmpty(t *testing.T) {
	s := newTestService(t, nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.5:54321"
	r.Header.Set("X-Forwarded-Preferred-Username", "mallory")

	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFromContext(r.Context()); ok {
			t.Errorf("expected no user resolved, got one")
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), r)
}

// TestHeader_IgnoredFromUntrustedIP covers the k8s case where the
// CIDR is set but the request bypassed the proxy — e.g. a
// port-forward or a misconfigured NetworkPolicy letting a pod
// outside the allowed range reach Rivolt directly. The header
// must be ignored; the fallback-to-cookie path takes over.
func TestHeader_IgnoredFromUntrustedIP(t *testing.T) {
	s := newTestService(t, []string{"10.42.0.0/16"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.168.1.5:443" // outside 10.42.0.0/16
	r.Header.Set("X-Forwarded-Preferred-Username", "mallory")

	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFromContext(r.Context()); ok {
			t.Errorf("expected no user for forged header from untrusted IP")
		}
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), r)
}

// TestHeader_HonouredFromTrustedIP is the happy path for the k8s
// deployment. The proxy sits in the configured CIDR and sets the
// header — Middleware resolves it into the same UUID the cookie
// path would.
func TestHeader_HonouredFromTrustedIP(t *testing.T) {
	s := newTestService(t, []string{"10.42.0.0/16"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.42.1.7:443"
	r.Header.Set("X-Forwarded-Preferred-Username", "alice")

	want := fakeUserIDFor("alice")
	var got uuid.UUID
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	handler.ServeHTTP(httptest.NewRecorder(), r)

	if got != want {
		t.Fatalf("resolved user = %s, want %s", got, want)
	}
}

// TestHeader_FallbackToEmail covers proxies that only emit the
// email header. Middleware should still produce a consistent
// UUID; the /api/auth/me contract is identical.
func TestHeader_FallbackToEmail(t *testing.T) {
	s := newTestService(t, []string{"10.42.0.0/16"})
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.42.1.7:443"
	r.Header.Set("X-Forwarded-Email", "alice@example.com")

	want := fakeUserIDFor("alice@example.com")
	var got uuid.UUID
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), r)

	if got != want {
		t.Fatalf("resolved user = %s, want %s", got, want)
	}
}

// TestCookie_RoundTrip exercises the issued-cookie path. IssueSession
// plants a cookie; a subsequent request carrying that cookie
// resolves to the same user. Stand-in for what an OIDC callback
// flow does after EnsureUserFull.
func TestCookie_RoundTrip(t *testing.T) {
	s := newTestService(t, nil)

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

// TestParseTrustedCIDRs_Errors makes sure operator typos surface
// at startup instead of silently disabling header auth.
func TestParseTrustedCIDRs_Errors(t *testing.T) {
	if _, err := ParseTrustedCIDRs("not-a-cidr"); err == nil {
		t.Fatalf("expected error on malformed CIDR")
	}
	nets, err := ParseTrustedCIDRs("")
	if err != nil || nets != nil {
		t.Fatalf("empty input: err=%v nets=%v, want nil/nil", err, nets)
	}
	nets, err = ParseTrustedCIDRs("10.0.0.0/8, 127.0.0.1/32")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(nets) != 2 {
		t.Fatalf("expected 2 nets, got %d", len(nets))
	}
	// Sanity: each parsed net contains an obvious member.
	want := map[string]net.IP{
		"10.0.0.0/8":   net.ParseIP("10.1.2.3"),
		"127.0.0.1/32": net.ParseIP("127.0.0.1"),
	}
	for _, n := range nets {
		ip := want[n.String()]
		if ip == nil || !n.Contains(ip) {
			t.Errorf("net %s: sanity containment failed", n)
		}
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

// Trust in the proxy that sets the user header is judged on the connection's
// own address, not on the visitor address the client-address middleware
// puts in RemoteAddr: a visitor behind the trusted proxy still gets in, and a
// stranger who forges X-Forwarded-For / CF-Connecting-IP with a trusted
// address does not become "trusted" (chi's RealIP used to allow exactly that).
func TestHeader_TrustUsesTheConnectionPeerNotTheClientAddress(t *testing.T) {
	s := newTestService(t, []string{"10.42.0.0/16"})
	resolved := func(remote string, hdr map[string]string) bool {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		r.Header.Set("X-Forwarded-Preferred-Username", "alice")
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		got := false
		clientip.Middleware(s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, got = UserFromContext(r.Context())
		}))).ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	if !resolved("10.42.0.172:5000", map[string]string{"CF-Connecting-IP": "203.0.113.9"}) {
		t.Error("a visitor behind the trusted proxy lost their identity (RemoteAddr was rewritten and then used for trust)")
	}
	if resolved("203.0.113.9:5000", map[string]string{"X-Forwarded-For": "10.42.0.5", "CF-Connecting-IP": "10.42.0.5", "X-Real-IP": "10.42.0.5"}) {
		t.Error("a stranger forged a trusted address and was believed")
	}
}
