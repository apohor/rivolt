// Package histwin carries the free plan's history window in a request context.
//
// A free account (Rivolt Pro, Sprint 14) sees drives, charges and statements from the first day of the calendar month Months back (UTC): one
// cutoff for all three, so the window is the current month and the Months before it (12 unless the deployment sets otherwise). The window lives only in the context of a request that a windowed route's middleware has stamped: background jobs
// (the recorder, recaps, the statement email), repairs, the export and every other context carry none, so they see everything. The stores
// (drives, charges) narrow their reads when a window is present; nothing here deletes or hides a row from storage.
//
// It has no dependencies so the stores can import it.
package histwin

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Months is how many calendar months before the current one the free window reaches back: the one number every route and client derives
// from (the cutoff, Money's month list, the 402 body, /api/auth/me history_months). It is a deployment setting (MonthsEnv, read once at
// start by Configure) and 12 when unset; clients never carry it.
var Months = 12

// MonthsEnv names the setting that holds Months.
const MonthsEnv = "RIVOLT_FREE_HISTORY_MONTHS"

// MaxMonths bounds the setting; the Trends route reaches back 24 months at most.
const MaxMonths = 24

// Configure applies the setting's value: empty keeps 12; otherwise a whole number of months from 1 to MaxMonths.
func Configure(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		Months = 12
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > MaxMonths {
		return fmt.Errorf("%s: %q is not a number of months from 1 to %d", MonthsEnv, raw, MaxMonths)
	}
	Months = n
	return nil
}

// LegacyMonths is the window an account keeps until the 30 days' notice (Terms section 11) has run, when the setting is below it: it was
// told "the last 12 months" when it signed up, and shrinking it takes something away from the tier it is on.
const LegacyMonths = 12

// NoticeAt is when the notice of the change to a shorter window went out (the new Terms, the email and the in-app notice). An account created
// at or after it signs up under the new Terms and gets Months at once; one created before it keeps LegacyMonths until EffectiveAt, which is
// NoticeAt plus the 30 days the Terms promise. Both are UTC instants. Unset (the zero value, the default) means the notice has not gone
// out, and every account keeps LegacyMonths whatever Months says: the setting is inert until the notice is set (RIVOLT_HISTORY_NOTICE_AT,
// read at start by ConfigureNotice).
var (
	NoticeAt    time.Time
	EffectiveAt time.Time
)

// NoticeEnv names the setting that holds the notice time (RFC 3339).
const NoticeEnv = "RIVOLT_HISTORY_NOTICE_AT"

// SetNotice sets NoticeAt and derives EffectiveAt; the zero time unsets both.
func SetNotice(at time.Time) {
	if at.IsZero() {
		NoticeAt, EffectiveAt = time.Time{}, time.Time{}
		return
	}
	NoticeAt = at.UTC()
	EffectiveAt = NoticeAt.AddDate(0, 0, 30)
}

// ConfigureNotice applies the notice setting's value: empty means no notice yet; anything else must be an RFC 3339 time.
func ConfigureNotice(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		SetNotice(time.Time{})
		return nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not an RFC 3339 time", NoticeEnv, raw)
	}
	SetNotice(at)
	return nil
}

// MonthsFor is the window length for an account created at created, at now. A setting of LegacyMonths or more applies to everyone. A shorter
// one applies only after the notice: until it is set everyone keeps LegacyMonths, and then new accounts get Months at once and the others
// at EffectiveAt. An unknown creation time (zero) counts as an existing account, so a failed lookup shows more history, never less.
func MonthsFor(created, now time.Time) int {
	if Months >= LegacyMonths {
		return Months
	}
	if NoticeAt.IsZero() {
		return LegacyMonths
	}
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
	// Months is how many calendar months back this window reaches.
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
