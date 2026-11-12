package api

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type signInNudgeKey struct{}

// withSignInNudge puts the sign-in nudge on the request context, where finishRivianConnect (the shared tail of the password and MFA legs) reads it.
func withSignInNudge(fn func(context.Context, uuid.UUID)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), signInNudgeKey{}, fn)))
		})
	}
}

func signInNudgeFrom(ctx context.Context) func(context.Context, uuid.UUID) {
	fn, _ := ctx.Value(signInNudgeKey{}).(func(context.Context, uuid.UUID))
	return fn
}

type receiptsSignInKey struct{}

// withReceiptsSignIn puts the receipts-sync hook on the request context for finishRivianConnect only (a completed sign-in, whatever the vehicle
// count). It is separate from the discovery nudge, which also runs on the unheld-refresh path that clients poll.
func withReceiptsSignIn(fn func(context.Context, uuid.UUID)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), receiptsSignInKey{}, fn)))
		})
	}
}

func receiptsSignInFrom(ctx context.Context) func(context.Context, uuid.UUID) {
	fn, _ := ctx.Value(receiptsSignInKey{}).(func(context.Context, uuid.UUID))
	return fn
}
