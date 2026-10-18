package clientip

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, remote string, hdr map[string]string) (remoteSeen, peerSeen string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		remoteSeen, peerSeen = r.RemoteAddr, Peer(r)
	})).ServeHTTP(httptest.NewRecorder(), req)
	return
}

// Two visitors behind the same trusted balancer get different addresses.
func TestTwoVisitorsBehindTheBalancerAreDifferent(t *testing.T) {
	a, pa := serve(t, "10.42.0.172:43210", map[string]string{"CF-Connecting-IP": "203.0.113.7"})
	b, _ := serve(t, "10.42.0.172:43211", map[string]string{"CF-Connecting-IP": "198.51.100.9"})
	if a != "203.0.113.7:43210" || b != "198.51.100.9:43211" || a == b {
		t.Errorf("remote addresses = %q, %q", a, b)
	}
	if pa != "10.42.0.172:43210" {
		t.Errorf("peer = %q, want the balancer", pa)
	}
	// IPv6 and IPv4-mapped forms.
	if v6, _ := serve(t, "10.42.0.172:1", map[string]string{"CF-Connecting-IP": "2001:db8::1"}); v6 != "[2001:db8::1]:1" {
		t.Errorf("ipv6 = %q", v6)
	}
	if m, _ := serve(t, "10.42.0.172:1", map[string]string{"CF-Connecting-IP": "::ffff:192.0.2.1"}); m != "192.0.2.1:1" {
		t.Errorf("mapped = %q", m)
	}
}

// A client that is not a trusted proxy cannot pick its own address with any
// forwarding header.
func TestUntrustedPeerCannotSpoof(t *testing.T) {
	hdr := map[string]string{
		"CF-Connecting-IP": "1.1.1.1", "X-Forwarded-For": "2.2.2.2, 3.3.3.3",
		"X-Real-IP": "4.4.4.4", "True-Client-IP": "5.5.5.5",
	}
	if got, _ := serve(t, "203.0.113.50:5555", hdr); got != "203.0.113.50:5555" {
		t.Errorf("public untrusted peer: %q, want it kept", got)
	}
	if got, _ := serve(t, "[2001:db8::50]:5555", hdr); got != "[2001:db8::50]:5555" {
		t.Errorf("public untrusted v6 peer: %q", got)
	}
}

// Forwarding headers other than CF-Connecting-IP never change the key, even
// from a trusted peer (X-Forwarded-For is client-controlled at its left end).
func TestOtherForwardingHeadersAreIgnored(t *testing.T) {
	got, _ := serve(t, "10.42.0.172:9", map[string]string{"X-Forwarded-For": "6.6.6.6", "X-Real-IP": "7.7.7.7", "True-Client-IP": "8.8.8.8"})
	if got != "10.42.0.172:9" {
		t.Errorf("remote = %q, want the peer", got)
	}
	// With the real header present, the others still do not matter.
	got, _ = serve(t, "10.42.0.172:9", map[string]string{"CF-Connecting-IP": "203.0.113.7", "X-Forwarded-For": "6.6.6.6"})
	if got != "203.0.113.7:9" {
		t.Errorf("remote = %q", got)
	}
}

// A bad CF-Connecting-IP from a trusted peer falls back to the peer, and a
// Cloudflare edge address is trusted like the balancer.
func TestBadHeaderFallsBackAndCloudflareIsTrusted(t *testing.T) {
	if got, _ := serve(t, "10.42.0.172:9", map[string]string{"CF-Connecting-IP": "not-an-ip"}); got != "10.42.0.172:9" {
		t.Errorf("bad header: %q", got)
	}
	if got, _ := serve(t, "172.67.1.1:443", map[string]string{"CF-Connecting-IP": "203.0.113.7"}); got != "203.0.113.7:443" {
		t.Errorf("cloudflare edge peer: %q", got)
	}
	if got, _ := serve(t, "10.42.0.172", map[string]string{"CF-Connecting-IP": "203.0.113.7"}); got != "203.0.113.7:0" {
		t.Errorf("bare-IP peer: %q", got)
	}
}
