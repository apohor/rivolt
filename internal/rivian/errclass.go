package rivian

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrorClass buckets every upstream failure into one of five
// categories. The classes drive very different downstream
// reactions: a Transient error should be retried with backoff; an
// Outage should be circuit-broken (phase 2); RateLimited should
// back off globally; UserAction should raise the per-user
// needs_reauth flag and stop retrying for that user; Unknown is
// treated as Transient-with-a-logged-warning.
//
// This enum is the single biggest determinant of support load at
// scale, per ROADMAP phase 1. Misclassifying UserAction as
// Transient means retrying a bad-password response until Rivian
// locks the account; misclassifying RateLimited as Transient
// means a reconnect storm that makes the throttle worse.
type ErrorClass int

const (
	// ClassUnknown is the default for anything we haven't seen
	// enough of to pattern-match confidently. Callers should
	// treat it as Transient but log at WARN so repeated unknowns
	// become a signal to extend Classify.
	ClassUnknown ErrorClass = iota

	// ClassTransient is network flaps, 5xx from a healthy
	// upstream (503 during a deploy), context timeouts on a
	// single call. Retry with jittered backoff.
	ClassTransient

	// ClassOutage is sustained 5xx, DNS failure, connection
	// refused. Distinct from Transient only in degree; in phase
	// 2 the circuit breaker trips on repeated Outage within a
	// window.
	ClassOutage

	// ClassRateLimited is 429 or a GraphQL error whose extension
	// code / message names a rate limit. Must never be retried
	// without honoring a global Redis token bucket (phase 2).
	ClassRateLimited

	// ClassUserAction means the user has to do something before
	// their calls will succeed again — log back in, accept an
	// MFA challenge, or contact Rivian support. Retrying is
	// actively harmful: repeated bad-password attempts escalate
	// to account lockouts. Raises the per-user needs_reauth
	// flag and suppresses further calls for that user.
	ClassUserAction
)

// String returns a short label suitable for logs and metrics
// labels. Keep stable — dashboards and alerts key on these.
func (c ErrorClass) String() string {
	switch c {
	case ClassTransient:
		return "transient"
	case ClassOutage:
		return "outage"
	case ClassRateLimited:
		return "rate_limited"
	case ClassUserAction:
		return "user_action"
	default:
		return "unknown"
	}
}

// UpstreamError is the structured error every outbound Rivian
// call returns when the upstream rejects us. It wraps the
// underlying cause (so errors.Is / errors.As keep working with
// net.OpError, context.Canceled, etc.) and carries the extras
// that make the retry/needs_reauth logic trivial to write.
//
// The struct is deliberately public so the api layer can unwrap
// it and turn 429/ClassRateLimited into a 503-with-Retry-After,
// ClassUserAction into a 401 that tells the client to open the
// Settings → Rivian pane, etc. Stringer is suitable for logs.
type UpstreamError struct {
	Class      ErrorClass
	Op         string // GraphQL operationName, e.g. "Login"
	HTTPStatus int    // 0 when the error is pre-HTTP (network, marshal)
	ExtCode    string // GraphQL extensions.code, when present
	// Reason is a short human hint for the user-facing UI or for
	// logs. For ClassUserAction it lands in users.needs_reauth_reason.
	Reason string
	// Cause is the underlying error. Preserved so callers can
	// keep using errors.Is on standard sentinels.
	Cause error
}

// Error implements error. Format is terse on purpose; the Class
// and Op travel separately through structured logging. The
// wrapped Cause is included so string-matching tests and log
// scrapers that look for upstream GraphQL messages keep working.
func (e *UpstreamError) Error() string {
	prefix := "rivian"
	if e.Op != "" {
		prefix = "rivian " + e.Op
	}
	parts := []string{prefix, e.Class.String()}
	if e.HTTPStatus != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.HTTPStatus))
	}
	if e.ExtCode != "" {
		parts = append(parts, "code="+e.ExtCode)
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if e.Cause != nil {
		parts = append(parts, e.Cause.Error())
	}
	return strings.Join(parts, ": ")
}

// Unwrap returns the underlying cause so errors.Is(err,
// context.Canceled), errors.Is(err, ErrUpstreamPaused), and
// similar continue to work through the UpstreamError wrapper.
func (e *UpstreamError) Unwrap() error { return e.Cause }

// IsUpstream reports whether err is an *UpstreamError with the
// given class. Convenience for call sites that only care about
// one class (e.g. "is this a user-action? flip needs_reauth").
func IsUpstream(err error, class ErrorClass) bool {
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		return false
	}
	return ue.Class == class
}

// ClassifyHTTP maps an HTTP status + optional response body
// fragment to an ErrorClass. Body is inspected only when the
// status alone is ambiguous (400 can be either user-action or
// transient depending on message).
//
// This is the narrow, pure-function seam so the classifier is
// trivially testable without a live gateway. doGraphQLAt calls
// it at one specific point in its pipeline.
func ClassifyHTTP(status int, body string) (ErrorClass, string) {
	switch {
	case status == 401 || status == 403:
		return ClassUserAction, statusReason(status, body)
	case status == 429:
		return ClassRateLimited, "rate limited"
	case status == 502 || status == 503 || status == 504:
		return ClassOutage, fmt.Sprintf("gateway %d", status)
	case status >= 500:
		return ClassOutage, fmt.Sprintf("server %d", status)
	case status == 400:
		// 400s from this gateway are usually malformed queries
		// (our bug), occasionally password-reject-before-MFA.
		// The body inspection below catches the user-action
		// variants; everything else is Transient so we retry
		// once and log — a real malformed-query bug will show
		// up as a repeated transient.
		if class, reason := classifyFromBody(body); class != ClassUnknown {
			return class, reason
		}
		return ClassTransient, "bad request"
	case status >= 400:
		// Other 4xx: default to UserAction so we don't retry
		// into an escalation. If we discover a retryable 4xx
		// later (409 conflict on some mutation, say) it moves
		// here explicitly.
		return ClassUserAction, statusReason(status, body)
	default:
		return ClassUnknown, ""
	}
}

// classifyFromBody inspects an error body for the telltale
// strings the Rivian gateway uses. It's deliberately liberal —
// false positives here raise needs_reauth unnecessarily, which
// is annoying but not dangerous; false negatives let us retry
// into a lockout, which is.
func classifyFromBody(body string) (ErrorClass, string) {
	if body == "" {
		return ClassUnknown, ""
	}
	lower := strings.ToLower(body)
	// User-action signals. All observed in real gateway
	// responses or in rivian-python-client's error handling.
	userActionMarkers := []struct {
		marker string
		reason string
	}{
		{"password", "password rejected"},
		{"credential", "credentials rejected"},
		{"unauthorized", "session expired"},
		{"unauthenticated", "session expired"},
		{"invalid_grant", "token revoked"},
		{"mfa", "MFA required"},
		{"otp_required", "MFA required"},
		{"account_locked", "account locked"},
		{"account locked", "account locked"},
		{"token expired", "token expired"},
	}
	for _, m := range userActionMarkers {
		if strings.Contains(lower, m.marker) {
			return ClassUserAction, m.reason
		}
	}
	// Rate-limit signals the body sometimes carries even when
	// the HTTP status is misleading (200 with a GraphQL error).
	if strings.Contains(lower, "rate limit") || strings.Contains(lower, "throttle") || strings.Contains(lower, "too many") {
		return ClassRateLimited, "rate limited"
	}
	return ClassUnknown, ""
}

// ClassifyNetwork maps a Go-side error from http.Client.Do,
// json.Unmarshal, or ctx cancellation into an ErrorClass. Called
// when the pipeline fails before we have an HTTP status to look
// at.
func ClassifyNetwork(err error) (ErrorClass, string) {
	if err == nil {
		return ClassUnknown, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ClassTransient, "context canceled"
	}
	var nerr net.Error
	if errors.As(err, &nerr) {
		if nerr.Timeout() {
			return ClassTransient, "network timeout"
		}
		return ClassOutage, "network error"
	}
	// DNS lookup failures don't always satisfy net.Error, but
	// their messages are distinctive. Cheaper than importing a
	// resolver probe.
	msg := err.Error()
	if strings.Contains(msg, "no such host") || strings.Contains(msg, "connection refused") {
		return ClassOutage, "dns/connect"
	}
	return ClassUnknown, ""
}

// ClassifyGraphQL maps the first GraphQL error in a 200-OK
// response envelope to an ErrorClass. The gateway will happily
// return HTTP 200 with errors in the body — a password reject
// during Login looks exactly like this.
func ClassifyGraphQL(extCode, message string) (ErrorClass, string) {
	// Extension codes the gateway uses (observed via
	// rivian-python-client v2.2.0 mitm analysis + local testing).
	switch strings.ToUpper(extCode) {
	case "UNAUTHENTICATED", "UNAUTHORIZED", "TOKEN_EXPIRED", "SESSION_EXPIRED":
		return ClassUserAction, "session expired"
	case "INVALID_CREDENTIALS", "AUTHENTICATION_FAILED":
		return ClassUserAction, "credentials rejected"
	case "MFA_REQUIRED", "OTP_REQUIRED":
		return ClassUserAction, "MFA required"
	case "RATE_LIMITED", "THROTTLED", "TOO_MANY_REQUESTS":
		return ClassRateLimited, "rate limited"
	case "INTERNAL_SERVER_ERROR":
		return ClassOutage, "upstream internal error"
	}
	// Fall back to the same body scan used for HTTP bodies.
	if class, reason := classifyFromBody(message); class != ClassUnknown {
		return class, reason
	}
	return ClassUnknown, ""
}

// statusReason picks a stable short reason for HTTP-status-only
// classifications so needs_reauth_reason is human-readable in
// the users row.
func statusReason(status int, body string) string {
	if class, reason := classifyFromBody(body); class == ClassUserAction && reason != "" {
		return reason
	}
	switch status {
	case 401:
		return "session expired"
	case 403:
		return "forbidden"
	}
	return fmt.Sprintf("HTTP %d", status)
}
