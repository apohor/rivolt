package ai

import (
	"context"
	"errors"
)

// Taste is the metering a free account's operator-key call goes through: a few answers a month on the operator's money. The
// provider takes it just before the model call, so a cached answer, a page load and a refused request never spend one.
type Taste interface {
	// Take reserves one unit before the call. Any error refuses the call: ErrTasteUsed and ErrTasteCeiling are the expected
	// ones, anything else means the store could not answer and the operator key stays closed.
	Take(ctx context.Context) error
	// Refund gives the unit back after a provider failure (not after an answer, a refusal or a cut-off one).
	Refund(ctx context.Context)
	// Spent reports what the call cost the operator, in dollars.
	Spent(ctx context.Context, usd float64)
}

var (
	// ErrTasteUsed means the account has taken this month's free answers.
	ErrTasteUsed = errors.New("AI free taste used up for this month")
	// ErrTasteCeiling means the install-wide daily ceiling on free-taste spend is reached.
	ErrTasteCeiling = errors.New("AI free taste unavailable until tomorrow")
)

type tasteCtxKey struct{}

// WithTaste marks ctx with the metering the operator key applies. Without a Taste on ctx the operator key refuses the call
// (ErrOperatorDenied): a route that forgot it fails closed.
func WithTaste(ctx context.Context, t Taste) context.Context {
	return context.WithValue(ctx, tasteCtxKey{}, t)
}

// TasteFrom returns the Taste on ctx.
func TasteFrom(ctx context.Context) (Taste, bool) {
	t, ok := ctx.Value(tasteCtxKey{}).(Taste)
	return t, ok && t != nil
}

type noTaste struct{}

func (noTaste) Take(context.Context) error     { return nil }
func (noTaste) Refund(context.Context)         {}
func (noTaste) Spent(context.Context, float64) {}

// NoTaste is the metering of an account that is not on the free taste: Pro (within its daily cap), the admin ping and the
// model evaluation.
var NoTaste Taste = noTaste{}

// WithUnmetered marks ctx as carrying a budget reservation and no taste metering.
func WithUnmetered(ctx context.Context) context.Context {
	return WithTaste(WithReservation(ctx), NoTaste)
}
