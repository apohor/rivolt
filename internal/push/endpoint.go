package push

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// A push subscription's endpoint is a URL the browser hands us and the server
// then POSTs to. It must be a real push service, not an address of the attacker's
// choosing (an internal host answering with a status and a body snippet is a
// request-forgery probe), so it is checked when it is stored and again before
// every send, and sends go through a client that follows no redirects, has a
// timeout and refuses to connect to private addresses.

// ErrEndpoint is returned for an endpoint that is not an accepted push service.
var ErrEndpoint = errors.New("push: endpoint is not an accepted push service")

// pushHosts are the web push services the browsers use: exact hosts, and
// suffixes (a leading dot) for the services that spread over subdomains.
var (
	pushHostsExact = map[string]bool{
		"fcm.googleapis.com":                true, // Chrome, Edge, Opera, Samsung
		"updates.push.services.mozilla.com": true, // Firefox
		"web.push.apple.com":                true, // Safari, iOS web apps
	}
	pushHostSuffixes = []string{
		".push.services.mozilla.com", // Firefox regional hosts
		".push.apple.com",            // Apple, incl. sandbox
		".notify.windows.com",        // Edge (WNS), wns2-*.notify.windows.com
	}
)

// ValidEndpoint reports whether raw is an https URL on an accepted push service:
// no credentials, the default port, a named host (never an IP literal) on the
// list above.
func ValidEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ErrEndpoint
	}
	if p := u.Port(); p != "" && p != "443" {
		return ErrEndpoint
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || net.ParseIP(host) != nil {
		return ErrEndpoint
	}
	if pushHostsExact[host] {
		return nil
	}
	for _, s := range pushHostSuffixes {
		if strings.HasSuffix(host, s) && len(host) > len(s) {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrEndpoint, host)
}

// privateAddr reports addresses a push send must never connect to.
func privateAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsMulticast() || a.IsUnspecified() || a.IsInterfaceLocalMulticast() ||
		// carrier-grade NAT and other non-global space
		netip.MustParsePrefix("100.64.0.0/10").Contains(a) || netip.MustParsePrefix("169.254.0.0/16").Contains(a) ||
		netip.MustParsePrefix("192.0.0.0/24").Contains(a) || netip.MustParsePrefix("198.18.0.0/15").Contains(a)
}

// refusePrivate is a net.Dialer Control: it runs on the resolved address just
// before connecting, so a hostname that resolves to a private address (or is
// re-pointed after the check) cannot be reached.
func refusePrivate(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ErrEndpoint
	}
	a, err := netip.ParseAddr(host)
	if err != nil || privateAddr(a) {
		return fmt.Errorf("%w: refused address", ErrEndpoint)
	}
	return nil
}

// newPushHTTPClient is the client every push send uses.
func newPushHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second, Control: refusePrivate}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return d.DialContext(ctx, network, addr)
			},
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: 10 * time.Second,
			MaxIdleConns:          20,
			IdleConnTimeout:       60 * time.Second,
			Proxy:                 nil, // never through an environment proxy
		},
		// A push service answers the POST itself; a redirect is never followed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// pushHTTPClient is shared so connections are reused.
var pushHTTPClient = newPushHTTPClient()
