// Package fence carries a group lease's fence token to the writes that must be refused when the
// lease is gone (the single-owner design, 4.3). A guarded write puts rivolt_lease_ok(group, pod,
// epoch) in the same statement as the write, so there is no check-then-write gap; the function is
// VOLATILE and reads statement_timestamp(), so a statement that starts after the lease expiry is
// refused even if nobody has taken the group yet.
package fence

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// ErrFenced means a write was refused because the pod no longer holds the group lease (or
// never did, or the pod's local lease deadline has passed). The caller stops acting for the
// group.
var ErrFenced = errors.New("fenced: group lease no longer held")

// Token is the proof a pod holds a group.
type Token struct {
	Group string
	Pod   string
	Epoch int64
}

type ctxKey struct{}

// With returns ctx carrying the token.
func With(ctx context.Context, t Token) context.Context {
	return context.WithValue(ctx, ctxKey{}, t)
}

// From returns the token in ctx, if any. A write with no token is unguarded: that is how
// everything outside the owner's recorder (imports, repairs, user-triggered writes) writes.
func From(ctx context.Context) (Token, bool) {
	t, ok := ctx.Value(ctxKey{}).(Token)
	return t, ok
}

// State is what a pod may do for a vehicle right now.
type State int

const (
	// Off: lead mode is off; writes are unguarded, as before M2.
	Off State = iota
	// Held: the pod holds the vehicle's group; writes carry the token.
	Held
	// Warming: the group was just taken from a holder that did not release it; writes wait out
	// the warm-up so the old holder's last commits land first.
	Warming
	// Lost: no live token (the group is not ours, or the local deadline passed); no write.
	Lost
)

// Source resolves a Rivian vehicle id to its fence state. The lease store implements it.
type Source interface {
	Fence(vehicleID string) (Token, State)
}

// TxCap bounds a guarded transaction from BEGIN: with the 2 s statement timeout and 1 s
// idle-in-transaction timeout below, an old owner's last commit lands within 3 s of the lease
// expiry (Postgres 16 has no transaction_timeout), which the new owner's 5 s warm-up covers.
const TxCap = 2 * time.Second

// BeginBounded starts a transaction for a guarded write: the context is capped at TxCap and the
// session gets SET LOCAL statement and idle-in-transaction timeouts. Callers use the returned
// context for the transaction's statements and call cancel when done.
func BeginBounded(ctx context.Context, db *sql.DB) (*sql.Tx, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, TxCap)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '2s'`); err != nil {
		_ = tx.Rollback()
		cancel()
		return nil, nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL idle_in_transaction_session_timeout = '1s'`); err != nil {
		_ = tx.Rollback()
		cancel()
		return nil, nil, nil, err
	}
	return tx, ctx, cancel, nil
}

// InTx runs fn in a bounded transaction (see BeginBounded) and commits it. Every guarded write
// that is not already a transaction goes through here, so a statement that passed the lease check
// and then waits (a row lock, a slow disk) is cancelled by the server after 2 s: an old owner's
// last write lands within about 3 s of the lease expiry, which the new owner's 5 s warm-up covers.
func InTx(ctx context.Context, db *sql.DB, fn func(ctx context.Context, tx *sql.Tx) error) error {
	tx, tctx, cancel, err := BeginBounded(ctx, db)
	if err != nil {
		return err
	}
	defer cancel()
	defer func() { _ = tx.Rollback() }()
	if err := fn(tctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Deadliner is the optional part of a Source that knows when a vehicle's local lease deadline falls:
// the monotonic moment 20 s after the last renew was sent, always before the database expiry. A
// Rivian call made after it must not go out, whatever the process was doing in between (a pause
// makes a context deadline fire on resume, where a poll of a flag would not).
type Deadliner interface {
	Deadline(vehicleID string) (time.Time, bool)
}
