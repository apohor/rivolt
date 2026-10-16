package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// cloudflareRanges are Cloudflare's published edge ranges. Source:
// https://www.cloudflare.com/ips-v4 and https://www.cloudflare.com/ips-v6
// (fetched 2026-10-16). Update them when Cloudflare publishes a change.
var cloudflareRanges = mustPrefixes(
	// IPv4
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	// IPv6
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
	"2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

func inCloudflare(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range cloudflareRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// directPeer is the address of whoever connected to the proxy in front
// of us (Cloudflare's edge, when the request came through Cloudflare).
// The rightmost X-Forwarded-For entry is what our nearest proxy
// appended or set, so a client-supplied header cannot move it. Without
// the header (direct access, tests) it is the connection address.
func directPeer(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if a, err := netip.ParseAddr(last); err == nil {
			return a.Unmap().String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// demoClientKey keys the per-client rate-limit bucket. CF-Connecting-IP
// is honoured only when the direct peer is a Cloudflare edge address:
// the origin is not provably reachable only through Cloudflare, so
// anyone else could set the header to dodge the limit. Anything else
// is keyed on the peer address itself.
func demoClientKey(r *http.Request) string {
	peer := directPeer(r)
	if pa, err := netip.ParseAddr(peer); err == nil && inCloudflare(pa) {
		if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
			observeDemoClientKey("cf")
			return ip.Unmap().String()
		}
	}
	observeDemoClientKey("peer")
	return peer
}

// demoClientKeyObserver, when set (once, at startup, before serving),
// counts which source keyed each demo request. No addresses are recorded.
var demoClientKeyObserver func(source string)

func observeDemoClientKey(source string) {
	if f := demoClientKeyObserver; f != nil {
		f(source)
	}
}
