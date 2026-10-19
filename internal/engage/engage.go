// Package engage is the one door for every non-transactional message (a plug-in
// reminder, a weekly recap, a milestone): it decides whether a message may go
// out at all and makes sure each one goes out at most once.
//
// Send checks, in order, each at send time: the user's toggle; quiet hours
// (pushes, 09:00 to 20:00 local); the budget (4 messages per account per rolling
// 30 days, push and email together); and then claims (account, kind, key) with
// an INSERT … ON CONFLICT DO NOTHING, so only the replica that wins the insert
// delivers, now and after a restart. A refused message leaves no row: it is
// counted in rivolt_engagement_sends_total and may be tried again under the
// same key. Charging-complete and software-update pushes are transactional and
// do not come through here.
//
// A Utility message is one the user scheduled themselves (the plug-in reminder).
// It is outside quiet hours and the budget, but it needs a time zone the user's
// browser stored (no UTC guess), never goes between 22:00 and 07:00 local, and
// is still claimed and recorded.
package engage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Channel is how a message is delivered.
type Channel string

const (
	Push  Channel = "push"
	Email Channel = "email"
)

// Outcomes, as the metric labels them.
const (
	Sent          = "sent"
	Failed        = "failed"
	AlreadySent   = "already_sent"
	SkippedOff    = "skipped_off"
	SkippedQuiet  = "skipped_quiet"
	SkippedBudget = "skipped_budget"
	SkippedNoTZ   = "skipped_no_tz"
	SkippedWindow = "skipped_window"
)

// Claim statuses a producer may record without sending (see Engage.Skip).
const (
	StatusEmpty    = "skipped_empty"
	StatusBackfill = "skipped_backfill"
)

// Policy numbers (DA's rulings).
const (
	MonthlyBudget      = 4
	BudgetWindow       = 30 * 24 * time.Hour
	QuietStartHour     = 9  // pushes go from 09:00 ...
	QuietEndHour       = 20 // ... to 20:00 local
	UtilityEarliest    = 7  // a utility never goes before 07:00 ...
	UtilityLatest      = 22 // ... or from 22:00 local
	StuckAfter         = 15 * time.Minute
	RetentionDays      = 400
	maxKindLen, maxKey = 40, 100
)

// Msg is one message to consider.
type Msg struct {
	User    uuid.UUID
	Kind    string // [a-z0-9_]
	Key     string // what makes this send unique per account, e.g. a local date
	Channel Channel
	// Utility: user-scheduled, outside the budget and quiet hours (see the
	// package comment).
	Utility bool
	// Toggle reads the user's live setting for this kind and channel. A nil
	// Toggle means off.
	Toggle func(ctx context.Context) (bool, error)
}

// Store is the persistence Engage needs.
type Store interface {
	// Timezone is the account's zone: the one its browser stored (stored=true,
	// known=true), else the statement email's (stored=false, known=true), else
	// none (known=false, tz "UTC" only so callers need not special-case it).
	Timezone(ctx context.Context, uid uuid.UUID) (tz string, stored, known bool, err error)
	// BudgetUsed counts budgeted claims (not skipped ones) since the cutoff.
	BudgetUsed(ctx context.Context, uid uuid.UUID, since time.Time) (int, error)
	// Claim inserts the (user, kind, key) row as 'sending'. For a budgeted
	// claim the insert also requires fewer than limit budgeted claims since the
	// cutoff, atomically; claimed=false with overBudget=true says that is why.
	Claim(ctx context.Context, m Msg, now, since time.Time, limit int) (claimed, overBudget bool, err error)
	// Mark ends a claim as 'sent' or 'failed'.
	Mark(ctx context.Context, uid uuid.UUID, kind, key, status string, now time.Time) error
	// RecordSkip inserts a finished row (skipped_empty, skipped_backfill); false
	// when the row already exists.
	RecordSkip(ctx context.Context, uid uuid.UUID, kind, key string, ch Channel, status string, now time.Time) (bool, error)
	// StuckSending counts claims left 'sending' for longer than d.
	StuckSending(ctx context.Context, d time.Duration, now time.Time) (int, error)
	// Purge deletes rows claimed before the cutoff.
	Purge(ctx context.Context, cutoff time.Time) (int64, error)
}

// Engage runs the checks. The zero value is not usable; set Store.
type Engage struct {
	Store Store
	Now   func() time.Time
	// Observe is told every outcome (kind, outcome) for the metric. May be nil.
	Observe func(kind, outcome string)
}

func (e *Engage) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engage) observe(kind, outcome string) {
	if e.Observe != nil {
		e.Observe(kind, outcome)
	}
}

// Send considers one message and, if every check passes and this caller wins
// the claim, delivers it. The outcome says what happened; the error is for
// infrastructure faults (a database error) or a delivery failure (then the
// outcome is Failed and the claim stays: it is not retried under this key).
func (e *Engage) Send(ctx context.Context, m Msg, deliver func(ctx context.Context) error) (string, error) {
	if m.User == uuid.Nil || m.Kind == "" || len(m.Kind) > maxKindLen || m.Key == "" || len(m.Key) > maxKey ||
		(m.Channel != Push && m.Channel != Email) || deliver == nil {
		return "", errors.New("engage: invalid message")
	}
	done := func(outcome string, err error) (string, error) {
		e.observe(m.Kind, outcome)
		return outcome, err
	}
	// 1. The toggle, read now.
	if m.Toggle == nil {
		return done(SkippedOff, nil)
	}
	on, err := m.Toggle(ctx)
	if err != nil {
		return done("", fmt.Errorf("engage: toggle: %w", err))
	}
	if !on {
		return done(SkippedOff, nil)
	}
	now := e.now()
	// 2. The clock. Pushes keep to 09:00-20:00 local, in a zone the account
	// has (its browser's, else the statement email's); a utility keeps to
	// 07:00-22:00 local and needs the browser's.
	if m.Utility || m.Channel == Push {
		tz, stored, known, err := e.Store.Timezone(ctx, m.User)
		if err != nil {
			return done("", fmt.Errorf("engage: timezone: %w", err))
		}
		// No zone at all is no guess either way: a budgeted push at UTC hours
		// would land in the middle of the night; a utility needs a zone the
		// browser stored.
		if !known || (m.Utility && !stored) {
			return done(SkippedNoTZ, nil)
		}
		loc, lerr := time.LoadLocation(tz)
		if lerr != nil {
			loc = time.UTC
			if m.Utility {
				return done(SkippedNoTZ, nil)
			}
		}
		h := now.In(loc).Hour()
		switch {
		case m.Utility && (h < UtilityEarliest || h >= UtilityLatest):
			return done(SkippedWindow, nil)
		case !m.Utility && (h < QuietStartHour || h >= QuietEndHour):
			return done(SkippedQuiet, nil)
		}
	}
	// 3. The budget, then 4. the claim (which re-checks the budget in the same
	// statement, so two kinds racing cannot both take the last slot).
	since := now.Add(-BudgetWindow)
	if !m.Utility {
		used, err := e.Store.BudgetUsed(ctx, m.User, since)
		if err != nil {
			return done("", fmt.Errorf("engage: budget: %w", err))
		}
		if used >= MonthlyBudget {
			return done(SkippedBudget, nil)
		}
	}
	claimed, over, err := e.Store.Claim(ctx, m, now, since, MonthlyBudget)
	if err != nil {
		return done("", fmt.Errorf("engage: claim: %w", err))
	}
	if !claimed {
		if over {
			return done(SkippedBudget, nil)
		}
		return done(AlreadySent, nil)
	}
	// 5. Deliver, then mark. A failed delivery is recorded and not retried.
	if derr := deliver(ctx); derr != nil {
		if merr := e.Store.Mark(ctx, m.User, m.Kind, m.Key, "failed", e.now()); merr != nil {
			return done(Failed, errors.Join(derr, merr))
		}
		return done(Failed, derr)
	}
	if merr := e.Store.Mark(ctx, m.User, m.Kind, m.Key, "sent", e.now()); merr != nil {
		return done(Sent, merr) // delivered; the row stays 'sending' and the stuck check reports it
	}
	return done(Sent, nil)
}

// Skip claims (user, kind, key) as finished without sending: a recap week with
// nothing in it, or a milestone already passed when the feature shipped. It
// reports whether this call created the row.
func (e *Engage) Skip(ctx context.Context, uid uuid.UUID, kind, key string, ch Channel, status string) (bool, error) {
	if status != StatusEmpty && status != StatusBackfill {
		return false, errors.New("engage: not a skip status")
	}
	ok, err := e.Store.RecordSkip(ctx, uid, kind, key, ch, status, e.now())
	if err == nil {
		out := status
		if !ok {
			out = AlreadySent
		}
		e.observe(kind, out)
	}
	return ok, err
}

// Maintain purges rows past retention and returns how many claims are stuck
// in 'sending' (a crash between claim and mark: never resent, so this is how a
// silently lost message becomes visible).
func (e *Engage) Maintain(ctx context.Context) (purged int64, stuck int, err error) {
	now := e.now()
	purged, err = e.Store.Purge(ctx, now.AddDate(0, 0, -RetentionDays))
	if err != nil {
		return 0, 0, err
	}
	stuck, err = e.Store.StuckSending(ctx, StuckAfter, now)
	return purged, stuck, err
}
