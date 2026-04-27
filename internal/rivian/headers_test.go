package rivian

import (
	"context"
	"testing"
)

// TestIOSHeadersOnEveryRequest is the contract that backs decision 8
// of ARCHITECTURE.md: every outbound Rivian request impersonates the
// iOS app. Regressing on any of these headers has historically been
// the fastest way to trip the gateway's bot-detection heuristics.
func TestIOSHeadersOnEveryRequest(t *testing.T) {
	g := newStubGateway(t)
	c := NewLive().WithEndpoint(g.srv.URL).WithRivoltVersion("v0.7.3")

	// ensureCSRF is the lightest operation that goes through
	// the shared request path; one call is enough to verify every
	// header is stamped on.
	if err := c.ensureCSRF(context.Background()); err != nil {
		t.Fatalf("ensureCSRF: %v", err)
	}

	if n := len(g.capturedReqs); n != 1 {
		t.Fatalf("want 1 captured request, got %d", n)
	}
	h := g.capturedReqs[0].Headers

	wantExact := map[string]string{
		"User-Agent":                   DefaultUserAgent,
		"Apollographql-Client-Name":    DefaultClientName,
		"Apollographql-Client-Version": DefaultClientVersion,
		"Accept":                       DefaultAccept,
		"Accept-Language":              "en-US,en;q=0.9",
		"Content-Type":                 "application/json",
		// X-Rivolt-Version is Rivolt's own identifier, injected so an
		// operator grepping upstream logs can tell Rivolt traffic
		// apart from a real iOS client. Must reflect what was passed
		// to WithRivoltVersion.
		"X-Rivolt-Version": "v0.7.3",
	}
	for k, want := range wantExact {
		if got := h.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}

	// Compile-time sanity: the Apollo-client identity we ship must
	// remain Android. We tried iOS in v0.10.0 and it triggered
	// server-side @defer on the WS subscription's gnssLocation
	// (see DefaultClientName comment in live.go). If someone bumps
	// these constants, this test is where they find out.
	if DefaultClientName != "com.rivian.android.consumer" {
		t.Errorf("DefaultClientName drifted: %q", DefaultClientName)
	}
	if DefaultClientVersion != "3.6.0-3989" {
		t.Errorf("DefaultClientVersion drifted: %q", DefaultClientVersion)
	}
}

// TestRivoltVersionFallback covers the default-empty path so a
// binary built without -ldflags still ships a non-empty
// X-Rivolt-Version.
func TestRivoltVersionFallback(t *testing.T) {
	c := NewLive().WithRivoltVersion("")
	if c.rivoltVersion != "dev" {
		t.Errorf("rivoltVersion = %q, want dev", c.rivoltVersion)
	}
}
