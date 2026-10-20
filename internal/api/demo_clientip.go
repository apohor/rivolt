package api

import (
	"github.com/apohor/rivolt/internal/clientip"
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

// directPeerFrom is directPeer plus whether the address came from the
// rightmost X-Forwarded-For entry (true) or the connection address (false).
func directPeerFrom(r *http.Request) (peer string, fromXFF bool) {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		last := strings.TrimSpace(parts[len(parts)-1])
		if a, err := netip.ParseAddr(last); err == nil {
			return a.Unmap().String(), true
		}
	}
	// The connection's own peer: the client-address middleware may have
	// rewritten RemoteAddr to the visitor, which this function must not see.
	remote := clientip.Peer(r)
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote, false
	}
	return host, false
}

// demoClientKey keys the per-client rate-limit bucket. CF-Connecting-IP
// is honoured when the direct peer is a Cloudflare edge address, or a
// private address: in our cluster the load balancer hop (klipper-lb /
// Traefik) masquerades, so the peer Traefik reports is always our own
// private LB address, never Cloudflare's. A PUBLIC non-Cloudflare peer
// cannot vouch for the header, so it is ignored and the peer address is
// the key.
//
// Residual risk, accepted: someone reaching the origin directly is
// SNATed to the same private address and could set the header to pick
// their own bucket. The install-wide budgets bound that, as they do for
// everything else here. The real fix is outside the app (no SNAT, or
// Cloudflare Authenticated Origin Pulls).
func demoClientKey(r *http.Request) string {
	peer, fromXFF := directPeerFrom(r)
	if pa, err := netip.ParseAddr(peer); err == nil {
		source := ""
		switch {
		case inCloudflare(pa):
			source = "cf"
		case isPrivatePeer(pa):
			source = "lb"
		}
		if source != "" {
			if ip, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("CF-Connecting-IP"))); err == nil {
				observeDemoClientKey(source)
				return ip.Unmap().String()
			}
		}
	}
	// The key is the peer address itself. Through the Cloudflare tunnel
	// the rightmost X-Forwarded-For entry is the visitor, which Cloudflare
	// appended (a public address outside its edge ranges): a per-visitor
	// key, counted as "xff". Without the header it is the connection
	// address ("peer"): behind an unforwarded proxy that is a shared key.
	if fromXFF {
		observeDemoClientKey("xff")
	} else {
		observeDemoClientKey("peer")
	}
	return peer
}

// isPrivatePeer reports whether a is a private-range or loopback
// address: 10/8, 172.16/12, 192.168/16, fc00::/7, 127/8, ::1.
func isPrivatePeer(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsPrivate() || a.IsLoopback()
}

// demoClientKeyObserver, when set (once, at startup, before serving),
// counts which source keyed each demo request. No addresses are recorded.
var demoClientKeyObserver func(source string)

func observeDemoClientKey(source string) {
	if f := demoClientKeyObserver; f != nil {
		f(source)
	}
}

// demoRateLimitedObserver, when set (once, at startup), counts demo 429s
// by route class (general, samples, heavy, tiles) and which budget bound
// (client or global). No addresses are recorded.
var demoRateLimitedObserver func(routeClass, scope string)

func demoDenied(routeClass, scope string) func() {
	return func() {
		if f := demoRateLimitedObserver; f != nil {
			f(routeClass, scope)
		}
	}
}
