// Package maps wires the same-origin proxy that lets the SPA reach
// a self-hosted OSRM (or any OSRM-compatible) routing service over
// the existing rivolt origin and session cookie.
//
// Why proxy instead of CORS-on-OSRM:
//
//   - OSRM 5.x has no native CORS. Putting the cluster service
//     behind its own ingress + traefik headers middleware works,
//     but burns a second cert + a second hostname for what is
//     effectively the SPA's own data path.
//   - Proxying through the rivolt API keeps drive maps same-origin,
//     reuses the existing auth middleware (route is mounted inside
//     the requireUser group), and lets us add per-user rate limits
//     later without touching the cluster.
//   - The frontend code change shrinks to "swap base URL"; no CORS
//     preflight handling, no second fetch credential mode.
//
// The proxy is intentionally dumb: it forwards GET /match/v1/...,
// /route/v1/..., /table/v1/... etc. unchanged to the upstream.
// Path-prefix stripping is done at mount time via http.StripPrefix
// in api.go, so this package only handles host rewrite and
// credential scrubbing.
package maps

import (
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// NewOSRMProxy builds a reverse-proxy handler that forwards to
// baseURL. The empty string returns (nil, nil) - callers (api.New)
// treat nil as "feature off" and skip mounting the route.
//
// baseURL is typically a cluster-internal Service address
// ("http://osrm.osrm.svc.cluster.local").
func NewOSRMProxy(baseURL string) (http.Handler, error) {
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
	defaultDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		defaultDirector(r)
		// Don't leak the rivolt session cookie or any bearer
		// auth header to the OSRM upstream. The router has
		// already authenticated the caller; the upstream has
		// no use for the credential and treating it as opaque
		// is the safer default.
		r.Header.Del("Cookie")
		r.Header.Del("Authorization")
		r.Host = u.Host
	}
	return proxy, nil
}

// ErrInvalidURL is returned when the configured base URL parses
// but is missing a scheme or host. Treated as fatal at startup so
// a typo in RIVOLT_OSRM_BASE_URL fails fast instead of silently
// dropping every drive map back to the public demo.
var ErrInvalidURL = errors.New("maps: OSRM base URL must include scheme and host")
