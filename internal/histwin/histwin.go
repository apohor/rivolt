// Package histwin carries the free plan's history window in a request context.
//
// A free account (Rivolt Pro, Sprint 14) sees drives, charges and statements from the first day of the calendar month Months back (UTC): one
// cutoff for all three, so the window is the current month and the Months before it. The window lives only in the context of a request that a windowed route's middleware has stamped: background jobs
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

// Months is how many calendar months before the current one the free window reaches back: the one number every client and every
// route derives from (the cutoff, Money's month list, the 402 body, /api/auth/me).
const Months = 6

// LegacyMonths is the window an account keeps until the 30 days' notice (Terms section 11) has run: it was told "the last 12 months"
// when it signed up, and shrinking it takes something away from the tier it is on.
const LegacyMonths = 12

// NoticeAt is when the notice of the 12 to 6 change goes out (the new Terms and the email). An account created at or after it signs up
// under the 6-month Terms and gets Months at once; one created before it keeps LegacyMonths until EffectiveAt.
// EffectiveAt is NoticeAt plus the 30 days the Terms promise. Both are UTC instants and are set once the owner has picked the date.
var (
	NoticeAt    = time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	EffectiveAt = NoticeAt.AddDate(0, 0, 30)
)

// MonthsFor is the window length for an account created at created, at now. An unknown creation time (zero) counts as an existing
// account, so a failed lookup shows more history, never less.
func MonthsFor(created, now time.Time) int {
	if !now.Before(EffectiveAt) || (!created.IsZero() && !created.Before(NoticeAt)) {
		return Months
	}
	return LegacyMonths
}

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
	// Months is how many calendar months back this window reaches (Months or LegacyMonths).
	Months int
	// Days is the window's length in days at the moment it was made (it changes through the month), for clients that show a number.
	Days int
}

// Free is the free plan's window at now for an account created at created (zero: unknown).
func Free(now, created time.Time) W {
	now = now.UTC()
	n := MonthsFor(created, now)
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	from := first.AddDate(0, -n, 0)
	return W{Since: from, MoneyFrom: from, Months: n, Days: int(now.Sub(from).Hours() / 24)}
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
