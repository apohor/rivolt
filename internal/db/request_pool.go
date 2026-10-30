package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// The request pool (row-level security, stage 2): the pool request handlers will use, connecting as the request
// role, with app.user_id set on every connection it hands out, from the query's context. Store code is
// unchanged: the setting rides on the connection, not on the query.
//
// Why it can't leak one user's setting into another's query: database/sql gets a fresh connection from pgxpool
// for every query and transaction, and pgxpool's PrepareConn hook sets the setting on each of those. That holds
// only because database/sql keeps no connections of its own here: no idle connections (it would reuse one
// without asking pgxpool) and no open-connection limit (at the limit it hands a released connection straight to
// a waiting query, again without pgxpool). pgxpool's MaxConns is the real limit, and its wait goes through the
// hook. OpenRequestPool sets both and nothing else may build this pool. It also assumes no transaction-mode
// pooler (PgBouncer, a CNPG Pooler) between the app and Postgres: a session setting would cross clients there.

// RequestConnHook is the PrepareConn hook: it sets app.user_id to the context's user, or to '' (no rows under
// the tenant policies) when there is none, on every connection before it's used.
func RequestConnHook(userFrom func(context.Context) (uuid.UUID, bool)) func(context.Context, *pgx.Conn) (bool, error) {
	return requestConnHook(userFrom, nil)
}

// requestConnHook is RequestConnHook plus noUser, called (with the acquire's context) each time a connection is handed out with
// no user: such a query sees no rows, so on a route that should have a user it is a missed route, and the count is stage 3's gate.
func requestConnHook(userFrom func(context.Context) (uuid.UUID, bool), noUser func(context.Context)) func(context.Context, *pgx.Conn) (bool, error) {
	return func(ctx context.Context, c *pgx.Conn) (bool, error) {
		id := ""
		if u, ok := userFrom(ctx); ok && u != uuid.Nil {
			id = u.String()
		} else if noUser != nil {
			noUser(ctx)
		}
		if _, err := c.Exec(ctx, `SELECT set_config('app.user_id', $1, false)`, id); err != nil {
			// false: destroy this connection rather than hand it out with an unknown setting.
			return false, fmt.Errorf("request pool: set app.user_id: %w", err)
		}
		return true, nil
	}
}

type requestPoolOptions struct{ noUser func(context.Context) }

// RequestPoolOption configures OpenRequestPool.
type RequestPoolOption func(*requestPoolOptions)

// WithNoUserCounter calls fn each time the pool hands out a connection for a context with no user. fn must be cheap and must not
// query the pool. The server passes the metrics counter, labelled by the route pattern of the request (never its path).
func WithNoUserCounter(fn func(context.Context)) RequestPoolOption {
	return func(o *requestPoolOptions) { o.noUser = fn }
}

// DefaultRequestMaxConns is the request pool's size per process. database/sql keeps no limit of its own here, so this is the
// real cap. The role has a connection limit (50 on preview) and Postgres has max_connections (100): replicas, plus one for the
// surge of a rolling update, times this must stay inside the role's limit, and with each pod's system pool (20) inside
// max_connections. 10 per pod keeps three pods at 30 of 50.
const DefaultRequestMaxConns int32 = 10

// RequestConnBudget is what the request pool may use of the role's connection limit (50): per pod times pods (replicas plus a
// surge pod) stays at or under it, leaving headroom for the grant loop and a hand-run migrate.
const RequestConnBudget = 45

// RequestPoolMaxConns reads RIVOLT_REQUEST_DB_MAX_CONNS (1 to 100); anything else is DefaultRequestMaxConns.
func RequestPoolMaxConns(getenv func(string) string) int32 {
	if n, err := strconv.Atoi(strings.TrimSpace(getenv("RIVOLT_REQUEST_DB_MAX_CONNS"))); err == nil && n >= 1 && n <= 100 {
		return int32(n)
	}
	return DefaultRequestMaxConns
}

// ErrRequestRoleBypassesRLS is returned when the request pool's role would see past the tenant policies: the
// tables' owner or a member of the owner role (which inherits its rights), a superuser, or a role with
// BYPASSRLS. Row-level security would be silently dormant, so the
// pool refuses to open.
var ErrRequestRoleBypassesRLS = errors.New("request pool: the role bypasses row-level security")

// RequestPool is the request pool: DB is what stores take; Close closes it and the pgxpool under it (which
// closing the *sql.DB alone doesn't).
type RequestPool struct {
	DB   *sql.DB
	pool *pgxpool.Pool
}

// Close closes the pool.
func (p *RequestPool) Close() {
	_ = p.DB.Close()
	p.pool.Close()
}

// OpenRequestPool opens the request pool at dsn (the request role's credentials) and checks the role: it
// returns ErrRequestRoleBypassesRLS (wrapped) if the role owns the tables, is a superuser or has BYPASSRLS.
// maxConns <= 0 means DefaultRequestMaxConns.
func OpenRequestPool(ctx context.Context, dsn string, maxConns int32, userFrom func(context.Context) (uuid.UUID, bool), opts ...RequestPoolOption) (*RequestPool, error) {
	var o requestPoolOptions
	for _, f := range opts {
		f(&o)
	}
	if userFrom == nil {
		return nil, errors.New("request pool: no user source")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("request pool: %w", err)
	}
	configureRequestPool(cfg, maxConns)
	cfg.PrepareConn = requestConnHook(userFrom, o.noUser)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("request pool: %w", err)
	}
	d := stdlib.OpenDBFromPool(pool)
	d.SetMaxIdleConns(0) // see the package note: database/sql must keep no connection of its own
	d.SetMaxOpenConns(0) // and never queue; pgxpool's MaxConns is the limit
	rp := &RequestPool{DB: d, pool: pool}
	if err := checkRequestRole(ctx, d); err != nil {
		rp.Close()
		return nil, err
	}
	return rp, nil
}

// configureRequestPool sizes the pgxpool: maxConns (the default when <= 0) and the idle timeout, so a
// quiet pod gives its connections back.
func configureRequestPool(cfg *pgxpool.Config, maxConns int32) {
	if maxConns <= 0 {
		maxConns = DefaultRequestMaxConns
	}
	cfg.MaxConns = maxConns
	cfg.MaxConnIdleTime = IdleConnTimeout
}

// checkRequestRole refuses a role that the tenant policies don't apply to.
func checkRequestRole(ctx context.Context, d *sql.DB) error {
	var role string
	var super, bypass, owns bool
	err := d.QueryRowContext(ctx, `
		SELECT r.rolname, r.rolsuper, r.rolbypassrls,
		       EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
		                  AND pg_has_role(r.oid, c.relowner, 'MEMBER'))
		  FROM pg_roles r WHERE r.rolname = current_user`).Scan(&role, &super, &bypass, &owns)
	if err != nil {
		return fmt.Errorf("request pool: check role: %w", err)
	}
	if super || bypass || owns {
		return fmt.Errorf("%w: %s (superuser %v, bypassrls %v, owns tables %v)", ErrRequestRoleBypassesRLS, role, super, bypass, owns)
	}
	return nil
}
