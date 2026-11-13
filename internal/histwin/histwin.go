// Package histwin carries the free plan's history window in a request context.
//
// A free account (Rivolt Pro, Sprint 14) sees drives, charges and statements from the first day of the calendar month Months back (UTC): one
// cutoff for all three, so the window is the current month and the twelve before it. The window lives only in the context of a request that a windowed route's middleware has stamped: background jobs
// (the recorder, recaps, the statement email), repairs, the export and every other context carry none, so they see everything. The stores
// (drives, charges) narrow their reads when a window is present; nothing here deletes or hides a row from storage.
//
// It has no dependencies so the stores can import it.
package histwin

import (
	"context"
	"errors"
	"time"
)

// Months is how many calendar months before the current one the free window reaches back.
const Months = 12

// ErrOutside is returned by a single-row read (a drive or charge by id) of a row older than the window. The row exists; the free plan
// does not show it.
var ErrOutside = errors.New("histwin: outside the free history window")

// W is a window. The zero value is no window.
type W struct {
	// Since is the oldest start time a drive, charge or other timed row may have to be shown: the first instant of the calendar month
	// Months back (UTC). Clients show it as free_from.
	Since time.Time
	// MoneyFrom is the same cutoff for the Money statements (a statement month is whole, so it is a month start).
	MoneyFrom time.Time
	// Days is the window's length in days at the moment it was made (it changes through the month), for clients that show a number.
	Days int
}

// Free is the free plan's window at now.
func Free(now time.Time) W {
	now = now.UTC()
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	from := first.AddDate(0, -Months, 0)
	return W{Since: from, MoneyFrom: from, Days: int(now.Sub(from).Hours() / 24)}
}

type key struct{}

// With returns ctx carrying w.
func With(ctx context.Context, w W) context.Context { return context.WithValue(ctx, key{}, w) }

// From is the window in ctx, if any.
func From(ctx context.Context) (W, bool) {
	w, ok := ctx.Value(key{}).(W)
	return w, ok && !w.Since.IsZero()
}

// Since is the window's oldest start time in ctx, if a window applies.
func Since(ctx context.Context) (time.Time, bool) {
	w, ok := From(ctx)
	return w.Since, ok
}

// Allows reports whether a row that started at t is inside the window in ctx (always, when there is none).
func Allows(ctx context.Context, t time.Time) bool {
	since, ok := Since(ctx)
	return !ok || !t.Before(since)
}

// Clamp raises a client-supplied lower bound to the window: the result is never earlier than the window's start. A zero from means "no
// lower bound" and becomes the window's start.
func Clamp(ctx context.Context, from time.Time) time.Time {
	since, ok := Since(ctx)
	if !ok || from.After(since) {
		return from
	}
	return since
}
