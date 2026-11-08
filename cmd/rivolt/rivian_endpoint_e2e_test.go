//go:build e2e_forward

package main

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestTaggedBuildAcceptsOnlyLoopbackEndpoints(t *testing.T) {
	resolve := func(m map[string][]string) func(context.Context, string) ([]net.IPAddr, error) {
		return func(_ context.Context, host string) ([]net.IPAddr, error) {
			ips, ok := m[host]
			if !ok {
				return nil, errors.New("no such host")
			}
			var out []net.IPAddr
			for _, s := range ips {
				out = append(out, net.IPAddr{IP: net.ParseIP(s)})
			}
			return out, nil
		}
	}
	lookup := resolve(map[string][]string{
		"localhost":   {"127.0.0.1", "::1"},
		"sneaky.test": {"127.0.0.1", "203.0.113.9"}, // one loopback address is not enough
		"remote.test": {"203.0.113.9"},
	})
	for _, ok := range []string{"http://127.0.0.1:9000/graphql", "http://[::1]:9000", "http://localhost:9000/gql", "https://127.5.5.5"} {
		if err := loopbackURL(context.Background(), ok, lookup); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"https://rivian.com/api/gql/gateway/graphql", "http://203.0.113.9:80", "http://sneaky.test", "http://remote.test", "http://unknown.test",
		"ftp://127.0.0.1", "127.0.0.1:9000", "http://user:pw@127.0.0.1", "http://", "http://0.0.0.0:9000", "http://10.0.0.5",
	} {
		if err := loopbackURL(context.Background(), bad, lookup); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestTaggedBuildReadsTheVariableAndRefusesARemoteHost(t *testing.T) {
	t.Setenv("RIVOLT_RIVIAN_ENDPOINT", "")
	if g, err := rivianEndpointOverride(); g != "" || err != nil {
		t.Errorf("unset: %q, %v", g, err)
	}
	t.Setenv("RIVOLT_RIVIAN_ENDPOINT", "http://127.0.0.1:9000/graphql")
	if g, err := rivianEndpointOverride(); g != "http://127.0.0.1:9000/graphql" || err != nil {
		t.Errorf("loopback: %q, %v", g, err)
	}
	t.Setenv("RIVOLT_RIVIAN_ENDPOINT", "https://rivian.com/x")
	if _, err := rivianEndpointOverride(); err == nil {
		t.Error("a remote host was accepted")
	}
}
