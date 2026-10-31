// Package entitlements answers "is this account Pro, and why": the rules of Rivolt Pro's plan, in one place.
//
// Rows (table entitlements, one per account and source) say what grants Pro; Compute turns them into a State at a moment in time. Service
// reads them (cached briefly) and adds the two cases that need no row: billing off (every self-hosted install; everyone is Pro and the
// table is never read) and the demo account (the showroom is always Pro). Nothing here writes: the rows are written by the billing webhooks,
// the sign-up trial, the launch-day founding grant and operator grants, each in its own item. Nothing here touches Rivian: the plan is
// tier-blind to the poller and the shared budget.
package entitlements

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/histwin"
)

// Sources a Pro grant can come from, plus the two that need no row.
const (
	SourceApple      = "apple"
	SourceStripe     = "stripe"
	SourceTrial      = "trial"
	SourceFounding   = "founding"
	SourceGrant      = "grant"
	SourceBillingOff = "billing_off" // billing is off on this install: everyone is Pro
	SourceDemo       = "demo"        // the demo account is the showroom
)

// Statuses of a row.
const (
	StatusActive  = "active"
	StatusPastDue = "past_due"
	StatusEnded   = "ended"
)

// Grace is how long a paid subscription whose renewal failed stays Pro after its period end.
const Grace = 7 * 24 * time.Hour

// Row is one entitlement row.
type Row struct {
	Source    string
	Status    string
	PeriodEnd *time.Time // nil: no end
	// CancelAtPeriodEnd is a paid subscription set to not renew: still Pro until PeriodEnd (plan_cancels_at).
	CancelAtPeriodEnd bool
	// UpdatedAt is when the row last changed; it dates an ended row that has no period end (a refund, a revoke).
	UpdatedAt time.Time
}

// State is the plan of one account at one moment.
type State struct {
	Pro bool
	// Source is what makes the account Pro: the strongest granting row (a paid one, then founding, grant, trial), billing_off or demo.
	// Empty when free.
	Source string
	// PeriodEnd is when that grant ends, grace included; nil when free or when it has no end.
	PeriodEnd *time.Time
	// TrialEnds is the trial's end, only while the trial is what grants Pro; nil otherwise.
	TrialEnds *time.Time
	// CancelsAt is when a paid subscription that is set to not renew stops granting Pro (its period end): "cancelled, paid until ...".
	// Only for an active stripe or apple row that is the one granting; nil otherwise.
	CancelsAt *time.Time
	// GraceEnds is when the grace of a paid subscription whose renewal failed runs out: "payment failed, update your card by ...".
	// Only while the row granting Pro is a past-due paid one (stripe or apple); nil otherwise.
	GraceEnds *time.Time
	// EndedAt and EndedSource describe a LAPSE: for a free account that once had Pro, when the most recent grant stopped and which source
	// it was ("Your Pro ended on ..."). Nil/empty for an account that never had Pro, and always for a Pro account.
	EndedAt     *time.Time
	EndedSource string
}

// rank orders the sources a client is told about when several grant Pro.
func rank(source string) int {
	switch source {
	case SourceStripe, SourceApple:
		return 4
	case SourceFounding:
		return 3
	case SourceGrant:
		return 2
	case SourceTrial:
		return 1
	}
	return 0
}

// grantEnd is when a row stops granting Pro, and whether it grants at all right now. An ended row never grants; an active one
// grants until its period end (for ever without one); a past-due paid one for Grace after it.
func grantEnd(r Row, now time.Time) (end *time.Time, grants bool) {
	switch r.Status {
	case StatusActive:
		if r.PeriodEnd == nil {
			return nil, true
		}
		return r.PeriodEnd, now.Before(*r.PeriodEnd)
	case StatusPastDue:
		if r.PeriodEnd == nil {
			return nil, false // a past-due row without an end is malformed: grant nothing
		}
		e := r.PeriodEnd.Add(Grace)
		return &e, now.Before(e)
	}
	return nil, false
}

// Compute is the plan from the rows at now. Several rows may grant at once (a trial and a subscription); the strongest source is
// reported, with its own end, and a client counting down a trial sees TrialEnds only while the trial is what grants.
func Compute(rows []Row, now time.Time) State {
	var best *Row
	var bestEnd *time.Time
	for i := range rows {
		end, ok := grantEnd(rows[i], now)
		if !ok {
			continue
		}
		if best == nil || rank(rows[i].Source) > rank(best.Source) ||
			(rank(rows[i].Source) == rank(best.Source) && later(end, bestEnd)) {
			best, bestEnd = &rows[i], end
		}
	}
	if best == nil {
		return State{EndedAt: lastEnded(rows, now), EndedSource: lastEndedSource(rows, now)}
	}
	st := State{Pro: true, Source: best.Source, PeriodEnd: bestEnd}
	if best.Source == SourceStripe || best.Source == SourceApple {
		switch {
		case best.Status == StatusPastDue:
			st.GraceEnds = bestEnd
		case best.Status == StatusActive && best.CancelAtPeriodEnd && bestEnd != nil:
			st.CancelsAt = bestEnd
		}
	}
	if best.Source == SourceTrial {
		st.TrialEnds = bestEnd
	}
	return st
}

// stoppedAt is when a row stopped granting Pro, if it has: a row past its end (grace included), or an ended row at its period end if that
// has passed, else when it was last updated. Nil for a row that still grants or never could.
func stoppedAt(r Row, now time.Time) *time.Time {
	switch r.Status {
	case StatusActive:
		if r.PeriodEnd != nil && !now.Before(*r.PeriodEnd) {
			return r.PeriodEnd
		}
	case StatusPastDue:
		if r.PeriodEnd != nil {
			if e := r.PeriodEnd.Add(Grace); !now.Before(e) {
				return &e
			}
		}
	case StatusEnded:
		if r.PeriodEnd != nil && !now.Before(*r.PeriodEnd) {
			return r.PeriodEnd
		}
		if !r.UpdatedAt.IsZero() {
			u := r.UpdatedAt
			return &u
		}
	}
	return nil
}

// lastEndedRow is the row that stopped most recently.
func lastEndedRow(rows []Row, now time.Time) (*Row, *time.Time) {
	var best *Row
	var bestAt *time.Time
	for i := range rows {
		if at := stoppedAt(rows[i], now); at != nil && (bestAt == nil || at.After(*bestAt)) {
			best, bestAt = &rows[i], at
		}
	}
	return best, bestAt
}

func lastEnded(rows []Row, now time.Time) *time.Time { _, at := lastEndedRow(rows, now); return at }

func lastEndedSource(rows []Row, now time.Time) string {
	if r, _ := lastEndedRow(rows, now); r != nil {
		return r.Source
	}
	return ""
}

// ActiveTrialEnd is the end of a running trial, whatever else the account has: a Stripe subscription bought during the trial starts
// billing at that moment (Stripe's trial_end), so the checkout asks for it here rather than from the display fields.
func ActiveTrialEnd(rows []Row, now time.Time) (time.Time, bool) {
	for _, r := range rows {
		if r.Source == SourceTrial && r.Status == StatusActive && r.PeriodEnd != nil && now.Before(*r.PeriodEnd) {
			return *r.PeriodEnd, true
		}
	}
	return time.Time{}, false
}

// later reports whether a ends after b, where nil means never.
func later(a, b *time.Time) bool {
	switch {
	case b == nil:
		return false
	case a == nil:
		return true
	}
	return a.After(*b)
}

// Service reads plans. The zero value is not usable; use New.
type Service struct {
	db       *sql.DB
	billing  bool
	demo     uuid.UUID
	ttl      time.Duration
	now      func() time.Time
	log      *slog.Logger
	onErr    func()
	mu       sync.Mutex
	cache    map[uuid.UUID]cached
	maxCache int
}

type cached struct {
	st Rowset
	at time.Time
}

// Rowset is the cached form: the rows, so a cache hit is recomputed at the current moment (a trial ending is seen on time).
type Rowset []Row

// Config for New.
type Config struct {
	// Billing is RIVOLT_BILLING: false (the default, every self-hosted install) makes every account Pro without reading the table.
	// Without a database there is nothing to read, so billing counts as off.
	Billing bool
	// Demo is the demo account's id (always Pro with billing on); uuid.Nil for none.
	Demo uuid.UUID
	// TTL is how long an account's rows are cached; 0 means DefaultTTL. A grant written on another replica is seen within it.
	TTL time.Duration
	Log *slog.Logger
	// OnLookupError is told each time the rows could not be read (the metric beside the fail-open warning).
	OnLookupError func()
}

// DefaultTTL bounds how stale a replica's view of an account's rows can be until cross-replica invalidation exists.
const DefaultTTL = 15 * time.Second

// New builds a Service over db (the pool the rows are read with).
func New(db *sql.DB, cfg Config) *Service {
	s := &Service{db: db, billing: cfg.Billing && db != nil, demo: cfg.Demo, ttl: cfg.TTL, now: time.Now, log: cfg.Log, onErr: cfg.OnLookupError,
		cache: map[uuid.UUID]cached{}, maxCache: 4096}
	if s.ttl <= 0 {
		s.ttl = DefaultTTL
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// Billing reports whether billing is on; clients hide every paywall control when it is not.
func (s *Service) Billing() bool { return s != nil && s.billing }

// State is the account's plan now. The error is the lookup's: callers decide what an unknown plan means (Pro below fails open).
func (s *Service) State(ctx context.Context, uid uuid.UUID) (State, error) {
	if s == nil || !s.billing {
		return State{Pro: true, Source: SourceBillingOff}, nil
	}
	if s.demo != uuid.Nil && uid == s.demo {
		return State{Pro: true, Source: SourceDemo}, nil
	}
	rows, err := s.rows(ctx, uid)
	if err != nil {
		s.lookupFailed(err)
		return State{}, err
	}
	return Compute(rows, s.now()), nil
}

// Pro is whether the account is Pro now. It fails OPEN: if the rows cannot be read, the account counts as Pro, because the cost of that
// is a free account briefly seeing more, while failing closed would hide a paying user's own history. The failure is logged.
func (s *Service) Pro(ctx context.Context, uid uuid.UUID) bool {
	st, err := s.State(ctx, uid)
	if err != nil {
		return true // State has already logged and counted the failure
	}
	return st.Pro
}

// lookupFailed logs and counts a failed read of the rows (rivolt_entitlements_lookup_errors_total): a sustained failure shows as a rate.
func (s *Service) lookupFailed(err error) {
	s.log.Warn("entitlements: lookup failed; an account is treated as Pro where the answer is needed", "err", err.Error())
	if s.onErr != nil {
		s.onErr()
	}
}

// Window is the history window that applies to the account now: the free plan's, or none for a Pro account, billing off, the demo
// account, and (failing open, like Pro) an account whose rows could not be read.
func (s *Service) Window(ctx context.Context, uid uuid.UUID) (histwin.W, bool) {
	if s.Pro(ctx, uid) {
		return histwin.W{}, false
	}
	return histwin.Free(s.now()), true
}

// TrialEnd is the end of the account's running trial (false: none). Billing off, the demo account and a failed lookup have none.
func (s *Service) TrialEnd(ctx context.Context, uid uuid.UUID) (time.Time, bool, error) {
	if s == nil || !s.billing || (s.demo != uuid.Nil && uid == s.demo) {
		return time.Time{}, false, nil
	}
	rows, err := s.rows(ctx, uid)
	if err != nil {
		return time.Time{}, false, err
	}
	end, ok := ActiveTrialEnd(rows, s.now())
	return end, ok, nil
}

// Invalidate drops the cached rows of one account on this replica (call it after writing one).
func (s *Service) Invalidate(uid uuid.UUID) {
	if s == nil {
		return
	}
	s.mu.Lock()
	delete(s.cache, uid)
	s.mu.Unlock()
}

func (s *Service) rows(ctx context.Context, uid uuid.UUID) (Rowset, error) {
	now := s.now()
	s.mu.Lock()
	if c, ok := s.cache[uid]; ok && now.Sub(c.at) < s.ttl {
		s.mu.Unlock()
		return c.st, nil
	}
	s.mu.Unlock()
	rs, err := s.db.QueryContext(ctx, `SELECT source, status, current_period_end, updated_at, cancel_at_period_end FROM entitlements WHERE user_id = $1`, uid)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out Rowset
	for rs.Next() {
		var r Row
		var end sql.NullTime
		if err := rs.Scan(&r.Source, &r.Status, &end, &r.UpdatedAt, &r.CancelAtPeriodEnd); err != nil {
			return nil, err
		}
		if end.Valid {
			t := end.Time
			r.PeriodEnd = &t
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if len(s.cache) >= s.maxCache {
		s.cache = map[uuid.UUID]cached{} // a bound, not an eviction policy: the table is small and a miss is one indexed read
	}
	s.cache[uid] = cached{st: out, at: now}
	s.mu.Unlock()
	return out, nil
}
