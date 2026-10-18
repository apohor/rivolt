// Package clientip decides which address a request really came from.
//
// Rivolt sits behind Cloudflare and an in-cluster load balancer, so the
// connection's peer address is the balancer (a 10.42.x.x pod address), the
// same for every visitor. The visitor's address arrives in CF-Connecting-IP,
// which is only believable when the connection came from a proxy we trust: a
// private-range address (our own balancer; it masquerades, so Cloudflare's
// edge is never the direct peer) or a Cloudflare edge address. From anyone
// else every forwarding header is ignored, so a client cannot choose its own
// rate-limit bucket, logged address or session address by sending
// CF-Connecting-IP, X-Forwarded-For, X-Real-IP or True-Client-IP.
//
// Middleware replaces chi's RealIP, which trusted those headers from anyone.
package clientip

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

type peerKey struct{}

// Middleware sets r.RemoteAddr to the real client address ("ip:port" form,
// port taken from the connection) and keeps the connection's own peer
// address in the context (see Peer).
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peer := r.RemoteAddr
		ctx := context.WithValue(r.Context(), peerKey{}, peer)
		if ip, ok := Resolve(peer, r.Header.Get("CF-Connecting-IP")); ok {
			_, port, _ := net.SplitHostPort(peer)
			if port == "" {
				port = "0"
			}
			r.RemoteAddr = net.JoinHostPort(ip, port)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Peer is the address of the connection itself (the proxy that connected),
// whatever Middleware rewrote RemoteAddr to. Trust decisions about the proxy
// (for example the trusted-proxy SSO headers) must use this, not RemoteAddr.
func Peer(r *http.Request) string {
	if p, ok := r.Context().Value(peerKey{}).(string); ok {
		return p
	}
	return r.RemoteAddr
}

// Resolve returns the client address for a connection from peer ("ip:port" or
// a bare IP) carrying the given CF-Connecting-IP value. ok is false when the
// peer is not a trusted proxy or the header is not a valid address: the
// caller keeps the peer.
func Resolve(peer, cfConnectingIP string) (ip string, ok bool) {
	host, _, err := net.SplitHostPort(peer)
	if err != nil {
		host = peer
	}
	pa, err := netip.ParseAddr(host)
	if err != nil || !trusted(pa) {
		return "", false
	}
	ca, err := netip.ParseAddr(strings.TrimSpace(cfConnectingIP))
	if err != nil {
		return "", false
	}
	return ca.Unmap().String(), true
}

func trusted(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsPrivate() || a.IsLoopback() {
		return true
	}
	for _, p := range cloudflareRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// cloudflareRanges are Cloudflare's published edge ranges
// (https://www.cloudflare.com/ips-v4, /ips-v6; fetched 2026-10-16).
var cloudflareRanges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
	"141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
	"197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
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
