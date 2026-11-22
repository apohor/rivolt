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

// LegacyMonths is the window an account created before the cutoff keeps, with no end date: it signed up under "the last 12 months", and
// shortening the setting takes nothing away from it.
const LegacyMonths = 12

// Cutoff is the instant from which a new account gets Months when the setting is below LegacyMonths: an account created at or after it
// has the shorter window, one created before it (or of unknown creation time) keeps LegacyMonths. A UTC instant, set in the deployment's
// values (CutoffEnv, read at start by ConfigureCutoff) together with the length, and the date the Terms name. Unset (the zero value, the
// default) means no account has the shorter window whatever Months says.
var Cutoff time.Time

// CutoffEnv names the setting that holds the cutoff (RFC 3339).
const CutoffEnv = "RIVOLT_FREE_HISTORY_CUTOFF"

// SetCutoff sets Cutoff (UTC); the zero time unsets it.
func SetCutoff(at time.Time) {
	if at.IsZero() {
		Cutoff = time.Time{}
		return
	}
	Cutoff = at.UTC()
}

// ConfigureCutoff applies the cutoff setting's value: empty means none; anything else must be an RFC 3339 time.
func ConfigureCutoff(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		SetCutoff(time.Time{})
		return nil
	}
	at, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not an RFC 3339 time", CutoffEnv, raw)
	}
	SetCutoff(at)
	return nil
}

// MonthsFor is the window length for an account created at created. A setting of LegacyMonths or more applies to everyone. A shorter one
// applies only to an account created at or after Cutoff; every other account keeps LegacyMonths, with no end date. An unknown creation
// time (zero) counts as an existing account, so a failed lookup shows more history, never less.
func MonthsFor(created time.Time) int {
	if Months >= LegacyMonths {
		return Months
	}
	if Cutoff.IsZero() || created.IsZero() || created.Before(Cutoff) {
		return LegacyMonths
	}
	return Months
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
	n := MonthsFor(created)
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
