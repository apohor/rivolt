package api

import (
	"context"

	"github.com/google/uuid"
)

// preDeleter is a step that must succeed (or be queued for retry) BEFORE an account's rows are deleted, because the cascade removes what the
// step needs: revoking the Sign in with Apple token, cancelling the Stripe subscriptions. An error means "do not delete the account".
type preDeleter interface {
	BeforeDelete(ctx context.Context, uid uuid.UUID) error
}

// preDeleters runs several in order and stops at the first error.
type preDeleters []preDeleter

func (p preDeleters) BeforeDelete(ctx context.Context, uid uuid.UUID) error {
	for _, s := range p {
		if s == nil {
			continue
		}
		if err := s.BeforeDelete(ctx, uid); err != nil {
			return err
		}
	}
	return nil
}

// preDeletes is every pre-deletion step of this server, in the order they run: Apple's token first, then Stripe's subscriptions. Both are
// nil-safe, so a server without either still deletes.
func (d Deps) preDeletes() preDeleters {
	var steps preDeleters
	if d.AppleRevocation != nil {
		steps = append(steps, d.AppleRevocation)
	}
	if d.StripeClosure != nil {
		steps = append(steps, d.StripeClosure)
	}
	return steps
}

// preDeleteFailedMessage is what a deletion answers when a pre-deletion step could neither finish nor queue its retry (the database is
// down): nothing was deleted, and the person can try again.
const preDeleteFailedMessage = "could not finish a step that must come before deletion (the Sign in with Apple token or the Stripe subscription) or queue it for retry; nothing was deleted"
