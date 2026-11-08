//go:build e2e_forward

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// rivianEndpointOverride reads RIVOLT_RIVIAN_ENDPOINT, the Rivian GraphQL gateway URL a test binary talks to instead of rivian.com. It exists
// ONLY in a binary built with -tags e2e_forward (the multi-pod forward e2e harness builds one itself); the default build, the release binaries
// and the images do not contain it, and a test asserts the default build ignores the variable. Even here it accepts loopback hosts only: the
// host must be an IP literal that is loopback, or a name every address of which (after resolution) is loopback. Anything else is refused, and
// the server does not start, so a tagged binary cannot be pointed at a host that would receive users' tokens.
func rivianEndpointOverride() (gateway string, err error) {
	raw := strings.TrimSpace(os.Getenv("RIVOLT_RIVIAN_ENDPOINT"))
	if raw == "" {
		return "", nil
	}
	if err := loopbackURL(context.Background(), raw, net.DefaultResolver.LookupIPAddr); err != nil {
		return "", fmt.Errorf("RIVOLT_RIVIAN_ENDPOINT refused: %w", err)
	}
	return raw, nil
}

// loopbackURL checks raw is an http(s) URL whose host is loopback. lookup resolves a name (net.DefaultResolver in the binary, a fake in tests).
func loopbackURL(ctx context.Context, raw string, lookup func(context.Context, string) ([]net.IPAddr, error)) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("not a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("the scheme must be http or https")
	}
	if u.User != nil {
		return errors.New("no credentials in the URL")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("no host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			return errors.New("the host is not a loopback address")
		}
		return nil
	}
	addrs, err := lookup(ctx, host)
	if err != nil || len(addrs) == 0 {
		return errors.New("the host does not resolve")
	}
	for _, a := range addrs {
		if !a.IP.IsLoopback() {
			return errors.New("the host resolves to an address that is not loopback")
		}
	}
	return nil
}
