package db

import (
	"context"
	"database/sql"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// newAccountTrial is the length of the Pro trial a brand-new account gets (0: none). main sets it when billing is on, before the server
// takes requests; with billing off nothing reads entitlements, so there is nothing to start.
var newAccountTrial atomic.Int64

// SetNewAccountTrial makes every brand-new account (not the demo) start a trial of this length. d <= 0 turns it off.
func SetNewAccountTrial(d time.Duration) { newAccountTrial.Store(int64(d)) }

// Execer is the slice of *sql.DB / *sql.Tx StartTrial needs.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// StartTrial gives uid a trial entitlement of length d from now, once: the (account, source) key makes a second call, a re-sign-in and a
// deleted-and-recreated row alike a no-op ("one trial per account, ever"). Reports whether it started one. It never charges: a trial is
// a row, not a subscription.
func StartTrial(ctx context.Context, d Execer, uid uuid.UUID, length time.Duration) (bool, error) {
	if d == nil || uid == uuid.Nil || length <= 0 {
		return false, nil
	}
	res, err := d.ExecContext(ctx, `
		INSERT INTO entitlements (user_id, source, status, current_period_end)
		VALUES ($1, 'trial', 'active', now() + make_interval(secs => $2))
		ON CONFLICT (user_id, source) DO NOTHING`, uid, length.Seconds())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// startNewAccountTrial is the sign-up hook of EnsureUserFull: best effort, a sign-up never fails because the trial row did not write.
func startNewAccountTrial(ctx context.Context, d Execer, uid uuid.UUID, username string) {
	length := time.Duration(newAccountTrial.Load())
	if length <= 0 || username == DemoUsername {
		return
	}
	if _, err := StartTrial(ctx, d, uid, length); err != nil {
		slog.WarnContext(ctx, "db: could not start the new account's trial", "user_id", uid.String(), "err", err.Error())
	}
}
