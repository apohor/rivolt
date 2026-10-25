package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
)

// openRequestSide returns the pool request handling uses (row-level security, stage 2). RIVOLT_REQUEST_DATABASE_URL unset (the
// default, and every deployment today) returns the system pool itself: request handling and background work share it, as before.
// Set, it names the request role's credentials: the role is granted what the migrations created (synchronously, before the pool
// opens), the pool refuses a role the tenant policies do not apply to, and request-side stores read and write as that role with
// app.user_id set from the request's user on every query.
//
// A misconfiguration is an error, never a quiet fall back to the system pool: a deployment that believes row-level security is
// on must not run with it off.
//
// Until the handlers are classified (S2-3, S2-4) the variable stays unset outside CI: every site that reads Deps.DB would run on
// the request pool, including login and admin paths that must use the system pool.
func openRequestSide(ctx context.Context, getenv func(string) string, system *sql.DB, onNoUser func(context.Context), log *slog.Logger) (*sql.DB, func(), error) {
	url := strings.TrimSpace(getenv("RIVOLT_REQUEST_DATABASE_URL"))
	if url == "" {
		return system, func() {}, nil
	}
	role := strings.TrimSpace(getenv("RIVOLT_APP_ROLE"))
	if role == "" {
		return nil, nil, errors.New("RIVOLT_REQUEST_DATABASE_URL needs RIVOLT_APP_ROLE (the role it connects as, whose grants the owner applies)")
	}
	if err := db.PrepareRequestRole(ctx, system, role); err != nil {
		return nil, nil, fmt.Errorf("request role %q: %w", role, err)
	}
	n := db.RequestPoolMaxConns(getenv)
	if n > 15 {
		// Not clamped: the replica count is not known in this process. Replicas plus a surge pod, times this, must stay under about
		// 45, inside the role's connection limit of 50, with headroom for the grant loop (DA).
		log.Warn("request pool size is large; replicas x size must stay under about 45", "max_conns", n)
	}
	rp, err := db.OpenRequestPool(ctx, url, n, auth.UserFromContext, db.WithNoUserCounter(onNoUser))
	if err != nil {
		return nil, nil, fmt.Errorf("request pool: %w", err)
	}
	log.Warn("request pool is ON (row-level security applies to request handling)", "role", role, "max_conns", n)
	return rp.DB, rp.Close, nil
}
