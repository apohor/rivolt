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
		Username:          "alice",
		Password:          "hunter2",
		CookieSecret:      []byte("0123456789abcdef0123456789abcdef"),
		TrustedProxyCIDRs: nets,
		UserIDFor:         fakeUserIDFor,
	}, func() (uuid.UUID, error) { return fakeUserIDFor("alice"), nil })
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

// TestCookie_RoundTrip exercises the local-login path. Login issues
// a cookie; a subsequent request carrying that cookie resolves to
// the same user.
func TestCookie_RoundTrip(t *testing.T) {
	s := newTestService(t, nil)

	loginReq := httptest.NewRequest("POST", "/api/auth/login",
		strings.NewReader(`{"username":"alice","password":"hunter2"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginRec := httptest.NewRecorder()
	s.Login(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200; body=%q", loginRec.Code, loginRec.Body.String())
	}
	cookies := loginRec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("no cookie issued on successful login")
	}

	followup := httptest.NewRequest("GET", "/", nil)
	for _, c := range cookies {
		followup.AddCookie(c)
	}
	want := fakeUserIDFor("alice")
	var got uuid.UUID
	handler := s.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = UserFromContext(r.Context())
	}))
	handler.ServeHTTP(httptest.NewRecorder(), followup)

	if got != want {
		t.Fatalf("cookie-resolved user = %s, want %s", got, want)
	}
}

// TestLogin_RejectsBadCredentials covers the timing-safe compare
// path — both wrong-user and wrong-password must yield the same
// 401 with the same body, so an attacker can't tell which side
// they got wrong.
func TestLogin_RejectsBadCredentials(t *testing.T) {
	s := newTestService(t, nil)
	cases := []string{
		`{"username":"alice","password":"wrong"}`,
		`{"username":"mallory","password":"hunter2"}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.Login(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("body=%s -> status %d, want 401", body, rec.Code)
		}
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
