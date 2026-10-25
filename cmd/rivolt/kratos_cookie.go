package main

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// kratosCookieResolver returns the Kratos session-cookie resolver only when RIVOLT_ACCEPT_KRATOS_COOKIE=true, else nil, which makes
// the auth middleware ignore an ory_kratos_session cookie entirely (the request is anonymous, and /api/auth/me does not count it as a
// credential). Default off: see the note at the call in main.
func kratosCookieResolver(getenv func(string) string, resolve func(ctx context.Context, cookieHeader string) (uuid.UUID, bool, error)) func(ctx context.Context, cookieHeader string) (uuid.UUID, bool, error) {
	if !strings.EqualFold(strings.TrimSpace(getenv("RIVOLT_ACCEPT_KRATOS_COOKIE")), "true") {
		return nil
	}
	return resolve
}
