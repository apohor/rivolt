package rivian

import (
	"context"
	"errors"
	"fmt"
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

// TestLoginDoesNotFlipNeedsReauthOn401 captures the contract that
// a failed Login (user typed the wrong password / their Rivian
// account is locked) must NOT raise the needs_reauth flag.
//
// The flag is for *previously good* sessions that have gone stale
// in the background; a user voluntarily submitting fresh
// credentials and being rejected is a different state, and
// flipping the flag traps brand-new OAuth signups (who have never
// had a Rivian session) in a "reconnect your Rivian account" UI
// that doesn't match their actual situation.
//
// Login is the only path that runs with withBypassReauth on its
// context, which is the marker doGraphQLAt uses to skip the
// markNeedsReauth call on classified UserAction errors.
func TestLoginDoesNotFlipNeedsReauthOn401(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
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
	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.WithReauthSink(func(_ context.Context, _ string) { sinkCalls++ })

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

	if needs, _ := c.NeedsReauth(); needs {
		t.Fatal("NeedsReauth: want false after a failed Login (bypassReauth honors)")
	}
	if sinkCalls != 0 {
		t.Errorf("sink calls: want 0 from failed Login, got %d", sinkCalls)
	}

	// Login bypass still permits a re-attempt — checkUpstream with
	// the bypass marker must let the next call through regardless
	// of the flag state.
	if err := c.checkUpstream(withBypassReauth(ctx)); err != nil {
		t.Fatalf("withBypassReauth must allow Login through, got %v", err)
	}
}

// TestBackgroundCallFlipsNeedsReauthOn401 covers the other side of
// the contract: a non-Login GraphQL call (no bypassReauth on the
// context) that gets a 401 MUST flip the flag, fire the sink, and
// cause the next call to short-circuit. This is the stale-session
// guard that stops the monitor pod from hammering Rivian after a
// user's session expires.
func TestBackgroundCallFlipsNeedsReauthOn401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	_, err := doGraphQLAt[map[string]any](ctx, c, c.endpoint,
		graphQLRequest{OperationName: "getUserInfo", Query: "query getUserInfo { ok }"},
		nil)
	if err == nil {
		t.Fatal("expected 401 to surface as error")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Class != ClassUserAction || ue.HTTPStatus != 401 {
		t.Fatalf("want ClassUserAction 401, got %v", err)
	}

	if needs, reason := c.NeedsReauth(); !needs || reason == "" {
		t.Fatalf("NeedsReauth: want true with reason after background 401, got needs=%v reason=%q", needs, reason)
	}
	if sinkCalls != 1 || sinkReason == "" {
		t.Errorf("sink: want 1 call with non-empty reason, got calls=%d reason=%q", sinkCalls, sinkReason)
	}
	if err := c.checkUpstream(ctx); !errors.Is(err, ErrNeedsReauth) {
		t.Fatalf("want ErrNeedsReauth on next call, got %v", err)
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

// TestIsAuthFlavoredReason is the explicit truth table. Every reason
// emitted by ClassifyHTTP / ClassifyGraphQL / classifyFromBody for a
// ClassUserAction outcome must be answered here, and every "HTTP NNN"
// fallback from statusReason must answer false (so a 404 / 422 / etc.
// doesn't poison needs_reauth).
func TestIsAuthFlavoredReason(t *testing.T) {
	cases := []struct {
		reason string
		want   bool
	}{
		// Real auth failures — should flip needs_reauth.
		{"session expired", true},
		{"password rejected", true},
		{"credentials rejected", true},
		{"token revoked", true},
		{"token expired", true},
		{"MFA required", true},
		{"account locked", true},
		{"forbidden", true},
		// Generic HTTP-status fallbacks from statusReason — must NOT
		// flip needs_reauth. ClassifyHTTP's catch-all for 4xx is
		// ClassUserAction with reason="HTTP <n>", which is exactly the
		// false-positive that flagged a working session as expired.
		{"HTTP 404", false},
		{"HTTP 422", false},
		{"HTTP 409", false},
		{"", false},
		// Reasons used for other classes — should be false too.
		{"rate limited", false},
		{"context canceled", false},
		{"network timeout", false},
		{"bad request", false},
	}
	for _, tc := range cases {
		if got := IsAuthFlavoredReason(tc.reason); got != tc.want {
			t.Errorf("IsAuthFlavoredReason(%q)=%v want %v", tc.reason, got, tc.want)
		}
	}
}

// TestNon401_4xxDoesNotPoisonReauth is the regression test for the
// bug where a multi-day trip leg getting a 404 from Rivian flipped
// the user's needs_reauth flag, locking them out of unrelated calls
// even though their session was still valid.
func TestNon401_4xxDoesNotPoisonReauth(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if strings.Contains(r.URL.Path, "gateway") && hits == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"c","appSessionToken":"a"}}}`)
			return
		}
		// 404 on the actual query — a non-auth 4xx that the
		// catch-all branch of ClassifyHTTP routes through
		// ClassUserAction but should NOT flip needs_reauth.
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"errors":[{"message":"vehicle not found"}]}`)
	}))
	defer server.Close()

	var sinkCalls int
	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.WithReauthSink(func(_ context.Context, _ string) { sinkCalls++ })

	err := c.Login(context.Background(), Credentials{Email: "a@b.c", Password: "pw"})
	if err == nil {
		t.Fatal("expected Login to fail with 404, got nil")
	}
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		t.Fatalf("want *UpstreamError, got %T: %v", err, err)
	}
	if ue.Class != ClassUserAction {
		t.Errorf("want ClassUserAction (catch-all 4xx), got %v", ue.Class)
	}
	// The whole point: needs_reauth must NOT have been flipped.
	if needs, _ := c.NeedsReauth(); needs {
		t.Error("needs_reauth flipped on non-auth 4xx — would lock user out of unrelated calls")
	}
	if sinkCalls != 0 {
		t.Errorf("sink fired on non-auth 4xx: want 0, got %d", sinkCalls)
	}
}

func TestErrCodeIsNotAvailableDescribe(t *testing.T) {
	forbidden := fmt.Errorf("GetChargingSchedule: %w", &UpstreamError{Class: ClassUserAction, Op: "GetChargingSchedule", ExtCode: "FORBIDDEN", Cause: errors.New("User is forbidden from accessing this resource")})
	if ErrCode(forbidden) != "FORBIDDEN" || !IsNotAvailable(forbidden) {
		t.Errorf("forbidden: code %q notAvailable %v", ErrCode(forbidden), IsNotAvailable(forbidden))
	}
	for _, e := range []error{
		&UpstreamError{ExtCode: "not_found"},
		&UpstreamError{ExtCode: "UNSUPPORTED"},
		&UpstreamError{HTTPStatus: 403},
		&UpstreamError{HTTPStatus: 404},
		&UpstreamError{HTTPStatus: 501},
	} {
		if !IsNotAvailable(e) {
			t.Errorf("%+v should be not available", e)
		}
	}
	for _, e := range []error{
		&UpstreamError{ExtCode: "RATE_LIMITED", HTTPStatus: 429},
		&UpstreamError{HTTPStatus: 500},
		&UpstreamError{ExtCode: "INTERNAL_SERVER_ERROR"},
		errors.New("plain"),
		nil,
	} {
		if IsNotAvailable(e) {
			t.Errorf("%v should not be not available", e)
		}
	}
	if ErrCode(errors.New("x")) != "" {
		t.Error("ErrCode of a non-upstream error must be empty")
	}
	// Describe: classification and the code, never the cause's text.
	d := Describe(forbidden)
	for _, leak := range []string{"forbidden from accessing", "User is"} {
		if strings.Contains(d, leak) {
			t.Errorf("Describe leaks the message text %q: %s", leak, d)
		}
	}
	if !strings.Contains(d, `code="FORBIDDEN"`) || !strings.Contains(d, "GetChargingSchedule") {
		t.Errorf("Describe = %s", d)
	}
	if got := Describe(errors.New("pq: secret value")); strings.Contains(got, "secret") {
		t.Errorf("Describe echoed an unknown error: %s", got)
	}
}

// A shared or authorized-driver account is expected to be refused (HTTP 403 or
// a FORBIDDEN code) on the charging-schedule and OTA reads. That must not flip
// needs_reauth, email the user or park monitoring; a 403 on an operation where
// it is not expected still does (review §4.3).
func TestExpectedForbiddenDoesNotFlipNeedsReauth(t *testing.T) {
	respond := map[string]func(http.ResponseWriter){
		"http403": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"errors":[{"message":"not allowed"}]}`)
		},
		"graphql FORBIDDEN": func(w http.ResponseWriter) {
			_, _ = io.WriteString(w, `{"errors":[{"message":"nope","extensions":{"code":"FORBIDDEN"}}]}`)
		},
	}
	for name, write := range respond {
		for _, tc := range []struct {
			op   string
			flip bool
		}{
			{"GetChargingSchedule", false},
			{"getOTAUpdateDetails", false},
			{"GetVehicleState", true},
		} {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { write(w) }))
			sinkCalls := 0
			c := NewLive()
			c.endpoint = server.URL + "/gateway"
			c.WithReauthSink(func(_ context.Context, _ string) { sinkCalls++ })
			_, err := doGraphQLAt[map[string]any](context.Background(), c, c.endpoint,
				graphQLRequest{OperationName: tc.op, Query: "query x { ok }"}, nil)
			server.Close()
			if err == nil {
				t.Fatalf("%s %s: expected an error", name, tc.op)
			}
			if !IsNotAvailable(err) {
				t.Errorf("%s %s: IsNotAvailable = false, the callers' back-off would not apply", name, tc.op)
			}
			// A GraphQL FORBIDDEN code has never been an auth failure; only the
			// HTTP 403 is, where it is not expected.
			want := tc.flip && name == "http403"
			if needs, _ := c.NeedsReauth(); needs != want || (sinkCalls > 0) != want {
				t.Errorf("%s %s: needs_reauth=%v sink calls=%d, want flip=%v", name, tc.op, needs, sinkCalls, want)
			}
		}
	}
}
