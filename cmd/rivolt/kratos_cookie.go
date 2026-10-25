package main

import (
	"context"
	"errors"
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

// checkKratosCookieConfig refuses a configuration that accepts the Kratos session cookie with no cookie host. The host guard is what
// keeps a parent-domain cookie from a sibling host (preview, demo) from signing someone in here; with it unset the guard is off, so
// turning the accept flag on alone would reopen that hole. Fail closed: the server does not start.
func checkKratosCookieConfig(getenv func(string) string) error {
	if kratosCookieResolver(getenv, func(context.Context, string) (uuid.UUID, bool, error) { return uuid.Nil, false, nil }) == nil {
		return nil // the cookie is ignored; the host is irrelevant
	}
	if strings.TrimSpace(getenv("RIVOLT_KRATOS_COOKIE_HOST")) == "" {
		return errors.New("RIVOLT_ACCEPT_KRATOS_COOKIE=true needs RIVOLT_KRATOS_COOKIE_HOST (the one host where Kratos issues its cookie, e.g. rivolt.dev): without it a cookie from a sibling host would sign people in; unset RIVOLT_ACCEPT_KRATOS_COOKIE or set the host")
	}
	return nil
}
