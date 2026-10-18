package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/apohor/rivolt/internal/clientip"
)

// limited returns a handler that applies the signup limiters behind the
// client-address middleware, as the router does.
func limited(perIP, global int) http.Handler {
	one, all := newSignupRequestLimiters(perIP, global)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	return clientip.Middleware(one.Middleware(all.Middleware(ok)))
}

func hit(h http.Handler, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/signup/request", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// Every visitor arrives from the balancer's address; each real client (the
// CF-Connecting-IP it sent) has its own bucket.
func TestSignupLimitIsPerVisitorBehindTheBalancer(t *testing.T) {
	h := limited(2, 1000)
	const lb = "10.42.0.172:50000"
	a := map[string]string{"CF-Connecting-IP": "203.0.113.1"}
	b := map[string]string{"CF-Connecting-IP": "203.0.113.2"}
	for i := 0; i < 2; i++ {
		if rec := hit(h, lb, a); rec.Code != 204 {
			t.Fatalf("visitor A request %d = %d", i+1, rec.Code)
		}
	}
	if rec := hit(h, lb, a); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("visitor A over its limit = %d, want 429", rec.Code)
	}
	// B, behind the very same balancer address, is not affected by A.
	for i := 0; i < 2; i++ {
		if rec := hit(h, lb, b); rec.Code != 204 {
			t.Fatalf("visitor B request %d = %d, want 204 (A's limit must not be shared)", i+1, rec.Code)
		}
	}
}

// A client that is not a trusted proxy cannot pick a fresh bucket by changing
// CF-Connecting-IP, X-Forwarded-For, X-Real-IP or True-Client-IP.
func TestSpoofedHeadersFromAnUntrustedPeerShareOneBucket(t *testing.T) {
	h := limited(2, 1000)
	const attacker = "198.51.100.77:4444"
	for i, hdr := range []map[string]string{
		{"CF-Connecting-IP": "1.1.1.1"},
		{"CF-Connecting-IP": "2.2.2.2", "X-Forwarded-For": "3.3.3.3"},
	} {
		if rec := hit(h, attacker, hdr); rec.Code != 204 {
			t.Fatalf("request %d = %d", i+1, rec.Code)
		}
	}
	for _, hdr := range []map[string]string{
		{"CF-Connecting-IP": "4.4.4.4"},
		{"X-Forwarded-For": "5.5.5.5, 6.6.6.6"},
		{"X-Real-IP": "7.7.7.7"},
		{"True-Client-IP": "8.8.8.8"},
		{},
	} {
		if rec := hit(h, attacker, hdr); rec.Code != http.StatusTooManyRequests {
			t.Errorf("spoofed %v got %d, want 429 (same key as the attacker's address)", hdr, rec.Code)
		}
	}
	// Spoofing X-Forwarded-For from a trusted peer does not move the key either.
	for i := 0; i < 2; i++ {
		hit(h, "10.42.0.172:1", map[string]string{"CF-Connecting-IP": "203.0.113.9"})
	}
	if rec := hit(h, "10.42.0.172:1", map[string]string{"CF-Connecting-IP": "203.0.113.9", "X-Forwarded-For": "9.9.9.9"}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("XFF changed the key from a trusted peer: %d", rec.Code)
	}
}

// The global backstop stops a flood spread over many visitors, and a request a
// visitor's own limit refused did not spend a global token.
func TestSignupGlobalBackstop(t *testing.T) {
	h := limited(5, 3)
	for i, ip := range []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"} {
		if rec := hit(h, "10.42.0.172:1", map[string]string{"CF-Connecting-IP": ip}); rec.Code != 204 {
			t.Fatalf("visitor %d = %d", i+1, rec.Code)
		}
	}
	if rec := hit(h, "10.42.0.172:1", map[string]string{"CF-Connecting-IP": "203.0.113.4"}); rec.Code != http.StatusTooManyRequests {
		t.Errorf("fourth visitor = %d, want 429 from the global backstop", rec.Code)
	}
}

// Retry-After is the time until the next token, not a fixed 60.
func TestRetryAfterIsHonest(t *testing.T) {
	h := limited(5, 1000)
	hdr := map[string]string{"CF-Connecting-IP": "203.0.113.50"}
	for i := 0; i < 5; i++ {
		hit(h, "10.42.0.172:1", hdr)
	}
	rec := hit(h, "10.42.0.172:1", hdr)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth = %d", rec.Code)
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || secs < 600 || secs > 721 { // one token every 720 s at 5 an hour
		t.Errorf("Retry-After = %q, want about 720", rec.Header().Get("Retry-After"))
	}
}
