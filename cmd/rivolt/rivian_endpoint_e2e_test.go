//go:build e2e_forward

package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
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
	if apply, err := rivianEndpointOverride(); apply != nil || err != nil {
		t.Errorf("unset: %v, %v", apply != nil, err)
	}
	t.Setenv("RIVOLT_RIVIAN_ENDPOINT", "http://127.0.0.1:9000/graphql")
	if apply, err := rivianEndpointOverride(); apply == nil || err != nil {
		t.Errorf("loopback: %v, %v", apply != nil, err)
	}
	t.Setenv("RIVOLT_RIVIAN_ENDPOINT", "https://rivian.com/x")
	if _, err := rivianEndpointOverride(); err == nil {
		t.Error("a remote host was accepted")
	}
}

// A name that resolves to loopback at boot and elsewhere on a later lookup (DNS rebinding) cannot be reached: the dial refuses it.
func TestTaggedTransportRefusesARebindingName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	var n atomic.Int32
	lookup := func(context.Context, string) ([]net.IPAddr, error) {
		if n.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.1")}}, nil
	}
	if err := loopbackURL(context.Background(), "http://rebind.test:"+port, lookup); err != nil {
		t.Fatalf("boot check: %v", err)
	}
	c := &http.Client{Transport: loopbackTransport(lookup)}
	if _, err := c.Get("http://rebind.test:" + port); err == nil {
		t.Fatal("a name that now resolves to 10.0.0.1 was dialled")
	}
	// A name that stays on loopback works, so the guard is not just failing everything.
	ok := func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	resp, err := (&http.Client{Transport: loopbackTransport(ok)}).Get("http://steady.test:" + port)
	if err != nil {
		t.Fatalf("loopback name refused: %v", err)
	}
	resp.Body.Close()
	// An IP literal that is not loopback is refused too, without any lookup.
	if _, err := (&http.Client{Transport: loopbackTransport(ok)}).Get("http://203.0.113.9:" + port); err == nil {
		t.Fatal("a non-loopback literal was dialled")
	}
}
