package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
)

// AppleRevocation is the Sign in with Apple step of every account deletion (siwa-design.md, DA conditions): before
// the account's rows go, its Apple refresh token is revoked at Apple, which also ends the person's authorization
// of Rivolt in their Apple ID settings. When Apple can't be reached, the revoke is queued in idp_delete_failures
// with the token sealed under the KEK envelope (the deleted account's id as associated data), so it still opens
// after the cascade removed user_secrets; RunAppleRevokeRetrier retries it, and drops it after 30 days or 20
// attempts. Only when both the revoke and the queueing fail does the deletion stop (the database is down then).
//
// The token is never logged, and neither is any Apple identifier.
type AppleRevocation struct {
	DB *sql.DB
	// Token reads the account's kept refresh token ("" when it has none): appleid.Store.RefreshToken.
	Token func(ctx context.Context, uid uuid.UUID) (string, error)
	// Revoke revokes a refresh token at Apple: oidc.Service.AppleRevoke. nil (no Apple provider) queues every
	// revoke, which then expires unless Apple is configured again within 30 days.
	Revoke func(ctx context.Context, refreshToken string) error
	Sealer crypto.Sealer
	Log    *slog.Logger
}

// appleRevokeTimeout bounds the revoke inside a deletion request.
const appleRevokeTimeout = 10 * time.Second

// BeforeDelete revokes uid's Apple token, or queues the revoke, and returns an error only when neither worked (the
// caller must then not delete the account). nil-safe: without an AppleRevocation there is nothing to do.
func (a *AppleRevocation) BeforeDelete(ctx context.Context, uid uuid.UUID) error {
	if a == nil || a.Token == nil {
		return nil
	}
	tok, err := a.Token(ctx, uid)
	if err != nil {
		return fmt.Errorf("apple revoke: read token: %w", err)
	}
	if tok == "" {
		return nil // never signed in with Apple, or no token kept
	}
	revokeErr := ErrNoAppleRevoker
	if a.Revoke != nil {
		rctx, cancel := context.WithTimeout(ctx, appleRevokeTimeout)
		revokeErr = a.Revoke(rctx, tok)
		cancel()
		if revokeErr == nil {
			return nil
		}
	}
	id, qerr := a.enqueue(context.WithoutCancel(ctx), uid, tok, revokeErr)
	if qerr != nil {
		a.logger().Error("apple revoke failed and could not be queued; account not deleted",
			"err", revokeErr.Error(), "queue_err", qerr.Error())
		return errors.Join(revokeErr, qerr)
	}
	a.logger().Warn("apple revoke failed; queued for retry", "failure_id", id.String(), "err", revokeErr.Error())
	return nil
}

// ErrNoAppleRevoker is the queued error when no Apple provider is configured to revoke with.
var ErrNoAppleRevoker = errors.New("apple revoke: no Apple provider configured")

func (a *AppleRevocation) enqueue(ctx context.Context, uid uuid.UUID, tok string, cause error) (uuid.UUID, error) {
	if a.DB == nil || a.Sealer == nil {
		return uuid.Nil, errors.New("no queue (database or sealer missing)")
	}
	sealed, err := a.Sealer.Seal(ctx, uid, []byte(tok))
	if err != nil {
		return uuid.Nil, fmt.Errorf("seal: %w", err)
	}
	return db.RecordAppleRevokeFailure(ctx, a.DB, uid, sealed, truncateErr(cause.Error()))
}

// Cancel drops a queued revoke for an account that turned out not to be deleted (the inactive purge's re-check).
// The revoke may already have happened at Apple; the person's next Apple sign-in authorizes Rivolt again.
func (a *AppleRevocation) Cancel(ctx context.Context, uid uuid.UUID) {
	if a == nil || a.DB == nil {
		return
	}
	if err := db.CancelAppleRevoke(ctx, a.DB, uid); err != nil {
		a.logger().Warn("apple revoke: cancel queued revoke failed", "err", err.Error())
	}
}

func (a *AppleRevocation) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}

// truncateErr bounds third-party error text stored and logged.
func truncateErr(msg string) string {
	if len(msg) > maxLoggedErr {
		return msg[:maxLoggedErr]
	}
	return msg
}

// RunAppleRevokeRetrier retries queued Apple revocations every interval until ctx ends, and drops the ones past 30
// days or 20 attempts. Safe on every replica (rows are claimed with SKIP LOCKED). No-op without a database.
func RunAppleRevokeRetrier(ctx context.Context, a *AppleRevocation, interval time.Duration) {
	if a == nil || a.DB == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.retryOnce(ctx)
		}
	}
}

// retryOnce runs one retry pass: expire, then retry what's due.
func (a *AppleRevocation) retryOnce(ctx context.Context) {
	if n, err := db.ExpireAppleRevokes(ctx, a.DB); err != nil {
		a.logger().Warn("apple revoke retry: expire failed", "err", err.Error())
	} else if n > 0 {
		a.logger().Warn("apple revoke retry: queued revokes expired unrevoked", "count", n)
	}
	if a.Revoke == nil || a.Sealer == nil {
		return // nothing to revoke with; rows wait (and expire)
	}
	due, err := db.ClaimDueAppleRevokes(ctx, a.DB, idpRetryBatch)
	if err != nil {
		a.logger().Warn("apple revoke retry: claim failed", "err", err.Error())
		return
	}
	for _, it := range due {
		tok, err := a.Sealer.Open(ctx, it.UserID, it.Sealed)
		if err != nil {
			// Can't open it (a retired KEK removed): nothing will ever revoke it, so drop it.
			a.logger().Error("apple revoke retry: sealed token unreadable; dropped", "failure_id", it.ID.String())
			_ = db.ResolveIdPDeleteFailure(ctx, a.DB, it.ID)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, appleRevokeTimeout)
		rerr := a.Revoke(rctx, string(tok))
		cancel()
		if rerr != nil {
			if err := db.AppleRevokeFailedAgain(ctx, a.DB, it.ID, truncateErr(rerr.Error())); err != nil {
				a.logger().Warn("apple revoke retry: record attempt failed", "failure_id", it.ID.String(), "err", err.Error())
			}
			continue
		}
		if err := db.ResolveIdPDeleteFailure(ctx, a.DB, it.ID); err == nil {
			a.logger().Info("apple revoke retry: resolved", "failure_id", it.ID.String(), "attempts", it.Attempts)
		}
	}
}
