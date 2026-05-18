// Package maps wires same-origin reverse-proxies that let the SPA
// reach self-hosted map services (Valhalla for routing + map
// matching, an nginx-served PMTiles bundle for vector basemaps)
// through the existing rivolt origin and session cookie.
//
// Why proxy instead of CORS-on-the-upstream:
//
//   - Most upstreams (Valhalla, the tile nginx) have no native CORS.
//     nginx can be configured for it, but exposing each upstream
//     behind its own ingress + cert burns hostnames for what is
//     effectively the SPA's own data path.
//   - Proxying through the rivolt API keeps drive maps same-origin,
//     reuses the existing auth middleware (routes are mounted inside
//     the requireUser group), and lets us add per-user rate limits
//     later without touching the cluster.
//   - The frontend code change shrinks to "swap base URL"; no CORS
//     preflight handling, no second fetch credential mode.
//
// The proxy is intentionally dumb: it forwards every request path
// unchanged to the upstream. Path-prefix stripping is done at mount
// time via http.StripPrefix in api.go, so this package only handles
// host rewrite and credential scrubbing.
package maps

import (
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// NewProxy builds a reverse-proxy handler that forwards to baseURL.
// The empty string returns (nil, nil) - callers (api.New) treat nil
// as "feature off" and skip mounting the route.
//
// baseURL is typically a cluster-internal Service address
// ("http://valhalla.valhalla.svc.cluster.local",
// "http://tiles.tiles.svc.cluster.local").
func NewProxy(baseURL string) (http.Handler, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, nil
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, ErrInvalidURL
	}

	proxy := httputil.NewSingleHostReverseProxy(u)
	// http.DefaultTransport caps MaxIdleConnsPerHost at 2. PMTiles
	// fetches the chargers archive via many parallel byte-range
	// requests; serialising them behind 2 keep-alive conns turns
	// "self-hosted in-cluster" into "slow as molasses." Bump the
	// pool and remove the per-host conn ceiling for tile traffic.
	proxy.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   64,
		MaxConnsPerHost:       0, // unlimited
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	defaultDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		defaultDirector(r)
		// Don't leak the rivolt session cookie or any bearer
		// auth header to the upstream. The router has already
		// authenticated the caller; the upstream has no use for
		// the credential and treating it as opaque is the safer
		// default.
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		r.Host = u.Host
	}
	return proxy, nil
}

// ErrInvalidURL is returned when a configured base URL parses but
// is missing a scheme or host. Treated as fatal at startup so a
// typo in RIVOLT_VALHALLA_BASE_URL / RIVOLT_TILES_BASE_URL fails
// fast instead of silently dropping every drive map back to public
// CDNs.
var ErrInvalidURL = errors.New("maps: base URL must include scheme and host")
