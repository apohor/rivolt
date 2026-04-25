// Package db owns the Rivolt Postgres connection pool, the shared
// migration runner, and a handful of identity helpers used by every
// store.
//
// # Identity model
//
// A Rivolt user is identified by a username — currently one static
// operator credential baked in via env (RIVOLT_USERNAME /
// RIVOLT_PASSWORD), with the intent that real auth (OIDC, Rivian
// SSO, …) will slot in behind the same seam later. The stable
// user_id is a deterministic UUIDv5 over the lowercased username,
// so the same login always resolves to the same tenant across
// restarts, exports and future federated installs.
//
// There is no default user. Stores expect a concrete user_id from
// an authenticated request context; unauthenticated writes are a
// bug, not a fallback.
//
// Runtime model
//
//   - One pgx pool per process. main opens it, hands *sql.DB to
//     every store. Stores never dial Postgres themselves.
//   - pgx is used through the database/sql stdlib adapter
//     (github.com/jackc/pgx/v5/stdlib) so store code keeps using
//     the stdlib *sql.DB API. The only Postgres-specific wart is
//     placeholder style — we keep `?` in SQL strings and rewrite
//     to `$1,$2,…` via Rebind at call time.
package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver registration
)

// userNamespace is the fixed UUID namespace used to hash usernames
// into stable v5 UUIDs. Changing this constant would re-key every
// user in every install, so don't — it's a published contract.
var userNamespace = uuid.MustParse("6b1f4c3e-6f4d-4f8a-9a7a-1d6d4b0a7a11")

// UserIDFor returns the stable v5 UUID for a username. Case-folded
// and whitespace-trimmed so "Alice", "alice" and " alice " all land
// on the same user — usernames are display-y, IDs are identity.
//
// Being deterministic means the same username produces the same
// UUID across every Rivolt install, which makes exports, imports
// and data migrations trivial: there is no lookup step.
func UserIDFor(username string) uuid.UUID {
	u := strings.ToLower(strings.TrimSpace(username))
	return uuid.NewSHA1(userNamespace, []byte(u))
}

// Open dials Postgres and returns a ready-to-use *sql.DB with every
// embedded migration applied. Safe to call once per process;
// subsequent callers should reuse the returned handle.
//
// No user is seeded — EnsureUser is called by the auth layer on a
// successful login / token validation instead. That keeps Open free
// of any identity concerns so it stays trivially testable.
//
// dsn accepts any DSN pgx understands — most commonly
// `postgres://user:pass@host:5432/dbname?sslmode=disable`.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, fmt.Errorf("DATABASE_URL is empty")
	}
	d, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open pgx: %w", err)
	}
	// Modest pool. Rivolt's workload is a single live state monitor
	// plus occasional UI queries — 20 is plenty and keeps us well
	// under Postgres's default 100-connection ceiling even with a
	// couple of replicas behind an LB.
	d.SetMaxOpenConns(20)
	d.SetMaxIdleConns(5)
	if err := d.PingContext(ctx); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := Migrate(ctx, d); err != nil {
		_ = d.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// EnsureUser upserts the user row for a username and returns the
// stable UUID. Idempotent — safe to call on every boot (for the
// static operator credential) and on every sign-in (for future
// real auth) without coordination.
//
// Returns an error if username is empty; the all-zero UUID would
// otherwise act as a silent multi-tenant footgun.
func EnsureUser(ctx context.Context, d *sql.DB, username string) (uuid.UUID, error) {
	return EnsureUserFull(ctx, d, username, "", "")
}

// EnsureUserFull is EnsureUser plus optional email and display_name
// columns. OIDC sign-in uses this so the user row reflects what the
// IdP knows (display_name in particular is what the UI surfaces in
// the account menu). Empty values are not written — a later sign-in
// that does carry them will fill the columns in, but a sign-in that
// doesn't (e.g. an IdP misconfigured to omit `name`) won't blank
// out a previously-good value.
func EnsureUserFull(ctx context.Context, d *sql.DB, username, email, displayName string) (uuid.UUID, error) {
	u := strings.ToLower(strings.TrimSpace(username))
	if u == "" {
		return uuid.Nil, fmt.Errorf("username is required")
	}
	id := UserIDFor(u)
	// COALESCE(NULLIF(EXCLUDED.x, ''), users.x) keeps an existing
	// non-empty value when the new sign-in supplies "". The empty-
	// string-as-null normalisation is in the application layer
	// because the column itself allows NULL.
	_, err := d.ExecContext(ctx, `
		INSERT INTO users (id, username, email, display_name)
		VALUES ($1, $2, NULLIF($3, ''), NULLIF($4, ''))
		ON CONFLICT (id) DO UPDATE SET
			username     = EXCLUDED.username,
			email        = COALESCE(NULLIF(EXCLUDED.email, ''),        users.email),
			display_name = COALESCE(NULLIF(EXCLUDED.display_name, ''), users.display_name)
	`, id, u, strings.TrimSpace(email), strings.TrimSpace(displayName))
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// Rebind rewrites `?` placeholders into Postgres `$N` style. Safe to
// call on a query that has no placeholders. Ignores `?` inside
// single- or double-quoted string literals so we don't clobber data.
//
// Kept cheap and inline because it runs on every query — ~a dozen
// instructions per placeholder. A sync.Map cache would complicate
// call sites for effectively zero wall-clock benefit on Rivolt's
// query volume.
func Rebind(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	var (
		out strings.Builder
		n   int
	)
	out.Grow(len(query) + 8)
	inSingle, inDouble := false, false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			out.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			out.WriteByte(c)
		case c == '?' && !inSingle && !inDouble:
			n++
			fmt.Fprintf(&out, "$%d", n)
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}
