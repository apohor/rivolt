package rivian

import (
	"strconv"
	"strings"
	"time"
)

// SessionLifetime is how long Rivian keeps a user session (u-sess) after sign-in. The
// 3.17.0 app has no way to extend one (no refresh or session-extension call), and the
// owner's session ended 180 days and about 15 minutes after it was made, so Rivolt treats
// it as a fixed 180 days from the sign-in (Session.AuthenticatedAt). The number is
// configurable at the sweep (RIVOLT_RIVIAN_SESSION_DAYS) because it is observed, not
// documented.
const SessionLifetime = 180 * 24 * time.Hour

// ExpiresAt is when the session is expected to stop working: its sign-in time plus
// lifetime. Zero when the sign-in time is unknown or there is no session.
func (s Session) ExpiresAt(lifetime time.Duration) time.Time {
	if s.UserSessionToken == "" || s.AuthenticatedAt.IsZero() {
		return time.Time{}
	}
	return s.AuthenticatedAt.Add(lifetime)
}

// ConfiguredSessionLifetime is the lifetime in force: SessionLifetime unless the operator
// set RIVOLT_RIVIAN_SESSION_DAYS (cmd/rivolt, at boot, before anything reads it).
var ConfiguredSessionLifetime = SessionLifetime

// SessionLifetimeFromEnv reads RIVOLT_RIVIAN_SESSION_DAYS (1 to 3650 days); anything else is
// SessionLifetime.
func SessionLifetimeFromEnv(getenv func(string) string) time.Duration {
	if n, err := strconv.Atoi(strings.TrimSpace(getenv("RIVOLT_RIVIAN_SESSION_DAYS"))); err == nil && n >= 1 && n <= 3650 {
		return time.Duration(n) * 24 * time.Hour
	}
	return SessionLifetime
}
