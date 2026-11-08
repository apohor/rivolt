//go:build e2e_forward

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/apohor/rivolt/internal/rivian"
)

// rivianEndpointOverride reads RIVOLT_RIVIAN_ENDPOINT, the base URL of a fake Rivian a test binary talks to instead of rivian.com. It exists
// ONLY in a binary built with -tags e2e_forward (the multi-pod forward e2e harness builds one itself); the default build, the release binaries
// and the images do not contain it, and a test asserts the default build ignores the variable.
//
// With the variable set, every Rivian URL moves to that base: the GraphQL gateway is <base>/gateway, the charging gateway <base>/chrg and the
// subscription socket <base>/ws (ws:// or wss://), so nothing is left pointing at rivian.com. Two guards keep fake-credential test traffic
// off the internet: the host is checked at boot (an early, friendly error: loopback IP literal, or a name whose every address is loopback),
// and the HTTP client that carries all of those requests refuses, at dial time, any connection to a non-loopback address, so a name that
// resolves elsewhere later (DNS rebinding) cannot get through either.
func rivianEndpointOverride() (func(*rivian.LiveClient) *rivian.LiveClient, error) {
	raw := strings.TrimSpace(os.Getenv("RIVOLT_RIVIAN_ENDPOINT"))
	if raw == "" {
		return nil, nil
	}
	if err := loopbackURL(context.Background(), raw, net.DefaultResolver.LookupIPAddr); err != nil {
		return nil, fmt.Errorf("RIVOLT_RIVIAN_ENDPOINT refused: %w", err)
	}
	base := strings.TrimRight(raw, "/")
	ws := "ws" + strings.TrimPrefix(base, "http") + "/ws"
	return func(lc *rivian.LiveClient) *rivian.LiveClient {
		h := &http.Client{Timeout: 30 * time.Second, Transport: loopbackTransport(net.DefaultResolver.LookupIPAddr)}
		return lc.WithHTTPClient(h).WithEndpoint(base+"/gateway").WithChargingEndpoint(base+"/chrg").WithWSEndpoint(ws, &http.Client{Transport: h.Transport})
	}, nil
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

// loopbackTransport is an HTTP transport that resolves each connection itself (lookup, a fake in tests) and refuses any address that is not
// loopback; the dialer's Control hook repeats the check on the address actually connected. No proxy is used.
func loopbackTransport(lookup func(context.Context, string) ([]net.IPAddr, error)) *http.Transport {
	d := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil || !net.ParseIP(host).IsLoopback() {
				return errors.New("refusing to connect to a non-loopback address")
			}
			return nil
		},
	}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var ips []net.IP
			if ip := net.ParseIP(host); ip != nil {
				ips = []net.IP{ip}
			} else {
				as, err := lookup(ctx, host)
				if err != nil {
					return nil, err
				}
				for _, a := range as {
					ips = append(ips, a.IP)
				}
			}
			if len(ips) == 0 {
				return nil, errors.New("no address")
			}
			for _, ip := range ips {
				if !ip.IsLoopback() {
					return nil, errors.New("refusing to connect to a non-loopback address")
				}
			}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
}
