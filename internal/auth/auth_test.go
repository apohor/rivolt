package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// TestHeader_IgnoredWhenCIDRListEmpty covers the homelab default.
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
