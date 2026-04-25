package rivian

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClassifyHTTP pins the HTTP-status → ErrorClass mapping. Each
// row documents the reason that class is correct, so a future
// change that bumps "401 → transient" has to defend why and what
// protection against account lockouts replaces it.
func TestClassifyHTTP(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   ErrorClass
	}{
		{"401 is user-action — bad/expired session", 401, "", ClassUserAction},
		{"403 is user-action — forbidden, retrying won't fix it", 403, "", ClassUserAction},
		{"429 is rate-limited — must back off", 429, "", ClassRateLimited},
		{"502 is outage — gateway up, origin broken", 502, "", ClassOutage},
		{"503 is outage — upstream deploying", 503, "", ClassOutage},
		{"504 is outage — upstream timeout", 504, "", ClassOutage},
		{"500 is outage — unknown server state", 500, "", ClassOutage},
		{"400 without marker is transient — likely a deploy window", 400, "", ClassTransient},
		{"400 with password body is user-action", 400, `{"error":"invalid password"}`, ClassUserAction},
		{"400 with MFA body is user-action", 400, `{"error":"mfa required"}`, ClassUserAction},
		{"409 defaults to user-action — don't retry into escalation", 409, "", ClassUserAction},
		{"200 (no HTTP error) is unknown — caller inspects body", 200, "", ClassUnknown},
	}
	for _, tc := range cases {
		got, _ := ClassifyHTTP(tc.status, tc.body)
		if got != tc.want {
			t.Errorf("%s: ClassifyHTTP(%d, %q) = %v, want %v", tc.name, tc.status, tc.body, got, tc.want)
		}
	}
}

// TestClassifyGraphQL covers the extensions.code hook — the
// primary classification signal when Rivian returns HTTP 200
// with a GraphQL errors envelope. Codes were harvested from the
// rivian-python-client v2.2.0 mitm analysis; adding a new code
// means adding a row here.
func TestClassifyGraphQL(t *testing.T) {
	cases := []struct {
		code, message string
		want          ErrorClass
	}{
		{"UNAUTHENTICATED", "", ClassUserAction},
		{"SESSION_EXPIRED", "", ClassUserAction},
		{"INVALID_CREDENTIALS", "", ClassUserAction},
		{"MFA_REQUIRED", "", ClassUserAction},
		{"RATE_LIMITED", "", ClassRateLimited},
		{"THROTTLED", "", ClassRateLimited},
		{"INTERNAL_SERVER_ERROR", "", ClassOutage},
		{"", "rate limit exceeded", ClassRateLimited},
		{"", "password is incorrect", ClassUserAction},
		{"", "entirely new kind of failure", ClassUnknown},
	}
	for _, tc := range cases {
		got, _ := ClassifyGraphQL(tc.code, tc.message)
		if got != tc.want {
			t.Errorf("ClassifyGraphQL(%q, %q) = %v, want %v", tc.code, tc.message, got, tc.want)
		}
	}
}

// TestClassifyNetwork covers the pre-HTTP failure path: context
// cancellation, timeouts, connection refused. These are the
// errors we see when the user is offline or the gateway IP is
// being deployed out. They must all classify as Transient or
// Outage — never UserAction, because the user didn't do anything.
func TestClassifyNetwork(t *testing.T) {
	if got, _ := ClassifyNetwork(context.Canceled); got != ClassTransient {
		t.Errorf("context.Canceled: got %v, want Transient", got)
	}
	if got, _ := ClassifyNetwork(context.DeadlineExceeded); got != ClassTransient {
		t.Errorf("context.DeadlineExceeded: got %v, want Transient", got)
	}
	dns := errors.New("dial tcp: lookup rivian.com: no such host")
	if got, _ := ClassifyNetwork(dns); got != ClassOutage {
		t.Errorf("dns failure: got %v, want Outage", got)
	}
	refused := errors.New("dial tcp 1.2.3.4:443: connect: connection refused")
	if got, _ := ClassifyNetwork(refused); got != ClassOutage {
		t.Errorf("connection refused: got %v, want Outage", got)
	}
}

// TestLiveClientFlipsNeedsReauthOn401 is the end-to-end wiring
// check: an outbound call that returns HTTP 401 must (1) return
// an UpstreamError of class ClassUserAction, (2) flip the
// in-memory needs_reauth mirror to true with a human-readable
// reason, (3) fire the reauthSink exactly once, (4) cause the
// next call to short-circuit with ErrNeedsReauth before any HTTP
// traffic is emitted.
func TestLiveClientFlipsNeedsReauthOn401(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// First request: handshake CSRF so the client is past
		// the bootstrap state. Second: the query we care about.
		if strings.Contains(r.URL.Path, "gateway") && hits == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"c","appSessionToken":"a"}}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer server.Close()

	var sinkCalls int
	var sinkReason string
	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.WithReauthSink(func(_ context.Context, reason string) {
		sinkCalls++
		sinkReason = reason
	})

	ctx := context.Background()
	err := c.Login(ctx, Credentials{Email: "a@b.c", Password: "pw"})
	if err == nil {
		t.Fatal("expected Login to fail with 401, got nil")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UpstreamError, got %T: %v", err, err)
	}
	if ue.Class != ClassUserAction {
		t.Errorf("want ClassUserAction, got %v", ue.Class)
	}
	if ue.HTTPStatus != 401 {
		t.Errorf("want HTTPStatus=401, got %d", ue.HTTPStatus)
	}

	// Mirror flipped.
	needs, reason := c.NeedsReauth()
	if !needs {
		t.Fatal("NeedsReauth: want true after 401")
	}
	if reason == "" {
		t.Error("NeedsReauth: want a non-empty reason")
	}

	// Sink fired once.
	if sinkCalls != 1 {
		t.Errorf("sink calls: want 1, got %d", sinkCalls)
	}
	if sinkReason == "" {
		t.Error("sink reason: want non-empty")
	}

	// Next call through the gate short-circuits with
	// ErrNeedsReauth before any HTTP traffic is emitted.
	// We can't route through Vehicles() here because the
	// client's session state is half-constructed after the
	// failed Login — but checkUpstream is the single chokepoint
	// every outbound call goes through, so exercising it
	// directly is the same guarantee.
	before := hits
	if err := c.checkUpstream(ctx); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("want ErrNeedsReauth, got %v", err)
	}
	if hits != before {
		t.Errorf("gate leaked HTTP traffic: before=%d after=%d", before, hits)
	}
}

// TestReauthSinkFiresOnceForStorm is the guard against a failure
// storm hammering the sink (and therefore Postgres). Ten
// consecutive 401s should fire the sink exactly once; clearing
// then re-flipping fires it again.
func TestReauthSinkFiresOnceForStorm(t *testing.T) {
	var sinkCalls int
	c := NewLive()
	c.WithReauthSink(func(_ context.Context, _ string) { sinkCalls++ })
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		c.markNeedsReauth(ctx, "session expired")
	}
	if sinkCalls != 1 {
		t.Fatalf("storm: want 1 sink call, got %d", sinkCalls)
	}

	// Clear → should fire once more with empty reason.
	c.clearNeedsReauth(ctx)
	if sinkCalls != 2 {
		t.Fatalf("clear: want 2 sink calls, got %d", sinkCalls)
	}

	// Re-flip → 3rd call.
	c.markNeedsReauth(ctx, "session expired")
	if sinkCalls != 3 {
		t.Fatalf("re-flip: want 3 sink calls, got %d", sinkCalls)
	}
}
