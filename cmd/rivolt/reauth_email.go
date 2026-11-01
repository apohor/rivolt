package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/logging"
)

// reauthEmailGrace is how long the one-shot re-auth email waits after the raise. A rejection that raises the flag can be undone within moments:
// the user signs in again, and the sign-in's save clears the flag (secrets.setSignedInAndClearFlag), including a stale rejection of the old
// session that landed between the sign-in's early clear and its save. Without a grace that user got "Action needed: reconnect your Rivian
// account" a few milliseconds before the sign-in finished.
const reauthEmailGrace = 20 * time.Second

// sendReauthEmailAfterGrace runs the email for ONE raise of the flag, started on the rising edge (the replica whose raise transitioned the flag;
// the others never get here). It reads when the flag was raised, waits the grace, reads again, and sends only if the user is STILL flagged with
// the SAME raise time: a flag cleared in between sends nothing, and a flag cleared and raised again (a new time) is left to the goroutine of the
// new raise, so one real raise sends one email.
//
// If the pod stops during the wait, or a read fails, nothing is sent. That is acceptable: the user stays flagged, and the existing nudge sweep
// emails them after its cooldown, because needs_reauth_notified_at was stamped when the flag was raised.
func sendReauthEmailAfterGrace(ctx context.Context, grace time.Duration, raisedAt func(context.Context) (time.Time, bool, error), send func(context.Context), logger *slog.Logger, uid uuid.UUID) {
	first, flagged, err := raisedAt(ctx)
	if err != nil {
		logger.Warn("reauth email: read the raise", "user_id", uid.String(), "err", logging.ScrubErr(err))
		return
	}
	if !flagged {
		return
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
		return // shutting down: the nudge sweep will email them
	}
	now, flagged, err := raisedAt(ctx)
	if err != nil {
		logger.Warn("reauth email: read the raise after the grace", "user_id", uid.String(), "err", logging.ScrubErr(err))
		return
	}
	if !flagged || !now.Equal(first) {
		logger.Info("reauth email skipped: the flag was cleared or raised again within the grace", "user_id", uid.String())
		return
	}
	send(context.Background()) // the send has its own timeout; a shutdown that starts now should not cut a mail half way
}
