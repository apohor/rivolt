package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	return func(ctx context.Context, c *pgx.Conn) (bool, error) {
		id := ""
		if u, ok := userFrom(ctx); ok && u != uuid.Nil {
			id = u.String()
		}
		if _, err := c.Exec(ctx, `SELECT set_config('app.user_id', $1, false)`, id); err != nil {
			// false: destroy this connection rather than hand it out with an unknown setting.
			return false, fmt.Errorf("request pool: set app.user_id: %w", err)
		}
		return true, nil
	}
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
// maxConns <= 0 means 20, the system pool's own limit.
func OpenRequestPool(ctx context.Context, dsn string, maxConns int32, userFrom func(context.Context) (uuid.UUID, bool)) (*RequestPool, error) {
	if userFrom == nil {
		return nil, errors.New("request pool: no user source")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("request pool: %w", err)
	}
	if maxConns <= 0 {
		maxConns = 20
	}
	cfg.MaxConns = maxConns
	cfg.PrepareConn = RequestConnHook(userFrom)
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

// checkRequestRole refuses a role that the tenant policies don't apply to.
func checkRequestRole(ctx context.Context, d *sql.DB) error {
	var role string
	var super, bypass, owns bool
	err := d.QueryRowContext(ctx, `
		SELECT r.rolname, r.rolsuper, r.rolbypassrls,
		       EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
		                  AND pg_has_role(r.oid, c.relowner, 'USAGE'))
		  FROM pg_roles r WHERE r.rolname = current_user`).Scan(&role, &super, &bypass, &owns)
	if err != nil {
		return fmt.Errorf("request pool: check role: %w", err)
	}
	if super || bypass || owns {
		return fmt.Errorf("%w: %s (superuser %v, bypassrls %v, owns tables %v)", ErrRequestRoleBypassesRLS, role, super, bypass, owns)
	}
	return nil
}
