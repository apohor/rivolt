// Package aitaste meters the free account's AI taste: a few answers a month from the operator's key (Pro v6).
//
// Two counters stand behind it. ai_taste_usage holds one row per (user, UTC month) on the request pool under row-level security;
// Reserve takes a unit atomically and Refund gives it back, so the count cannot pass the quota however the calls race. ai_taste_spend
// holds what the day's free-taste calls may still cost the operator, on the system pool: a hold at the route's estimate is taken before
// the call and settled to the real cost after it. Both fail CLOSED: a store error refuses the call.
package aitaste

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ai"
)

const (
	// MonthlyQuota is how many operator-key answers a free account takes in a UTC calendar month.
	MonthlyQuota = 5
	// DefaultDailyCeilingUSD is what free-taste calls may cost the operator in a UTC day, on top of the Console spend limit.
	DefaultDailyCeilingUSD = 5.0
	// EstimateUSD is held per call before it runs and settled to the real cost after. It sits above the dearest measured
	// operator answer (about four cents on the efficiency route).
	EstimateUSD = 0.06
)

const micro = 1e6

func microUSD(usd float64) int64 { return int64(math.Round(usd * micro)) }

// MonthStart is the first day of now's UTC calendar month, the key of ai_taste_usage.
func MonthStart(now time.Time) time.Time {
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// ResetAt is when the month's taste renews: the first instant of the next UTC month.
func ResetAt(now time.Time) time.Time { return MonthStart(now).AddDate(0, 1, 0) }

// DayStart is the UTC day, the key of ai_taste_spend.
func DayStart(now time.Time) time.Time {
	n := now.UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// NextDay is when the daily ceiling renews.
func NextDay(now time.Time) time.Time { return DayStart(now).AddDate(0, 0, 1) }

// Store counts a user's taste on the request pool.
type Store struct{ db *sql.DB }

// NewStore wires a Store to the request pool. A nil pool yields a nil Store.
func NewStore(req *sql.DB) *Store {
	if req == nil {
		return nil
	}
	return &Store{db: req}
}

// Reserve takes one unit of the month's quota for uid, and reports false (and no error) when the quota is spent. The pool's
// user must be uid: the row is under row-level security.
func (s *Store) Reserve(ctx context.Context, uid uuid.UUID, now time.Time, quota int) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("aitaste: no store")
	}
	if uid == uuid.Nil {
		return false, errors.New("aitaste: zero user")
	}
	var n int
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO ai_taste_usage (user_id, month, n) VALUES ($1, $2, 1)
		ON CONFLICT (user_id, month) DO UPDATE SET n = ai_taste_usage.n + 1 WHERE ai_taste_usage.n < $3
		RETURNING n`, uid, MonthStart(now), quota).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("aitaste: reserve: %w", err)
	}
	return true, nil
}

// Refund gives one unit back, never below zero.
func (s *Store) Refund(ctx context.Context, uid uuid.UUID, now time.Time) error {
	if s == nil || s.db == nil || uid == uuid.Nil {
		return errors.New("aitaste: nothing to refund")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE ai_taste_usage SET n = GREATEST(n - 1, 0) WHERE user_id = $1 AND month = $2`, uid, MonthStart(now))
	return err
}

// Used is how many units uid has taken this month.
func (s *Store) Used(ctx context.Context, uid uuid.UUID, now time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT n FROM ai_taste_usage WHERE user_id = $1 AND month = $2`, uid, MonthStart(now)).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// Spend is the install-wide daily ceiling on free-taste cost, on the system pool.
type Spend struct {
	db           *sql.DB
	ceilingMicro int64
}

// NewSpend wires the ceiling to the system pool. A nil pool yields a nil Spend, which refuses every hold.
func NewSpend(sys *sql.DB, ceilingUSD float64) *Spend {
	if sys == nil {
		return nil
	}
	return &Spend{db: sys, ceilingMicro: microUSD(ceilingUSD)}
}

// Hold adds est to the day's spend unless that would pass the ceiling.
func (sp *Spend) Hold(ctx context.Context, now time.Time, est float64) (bool, error) {
	if sp == nil || sp.db == nil {
		return false, errors.New("aitaste: no spend store")
	}
	e := microUSD(est)
	if e > sp.ceilingMicro {
		return false, nil
	}
	var total int64
	err := sp.db.QueryRowContext(ctx, `
		INSERT INTO ai_taste_spend (day, micro_usd) VALUES ($1, $2)
		ON CONFLICT (day) DO UPDATE SET micro_usd = ai_taste_spend.micro_usd + $2 WHERE ai_taste_spend.micro_usd + $2 <= $3
		RETURNING micro_usd`, DayStart(now), e, sp.ceilingMicro).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("aitaste: hold: %w", err)
	}
	return true, nil
}

// Adjust moves the day's spend by delta (a settlement: real cost minus the hold; a release: minus the hold). Never below zero.
func (sp *Spend) Adjust(ctx context.Context, day time.Time, deltaUSD float64) error {
	if sp == nil || sp.db == nil {
		return errors.New("aitaste: no spend store")
	}
	_, err := sp.db.ExecContext(ctx, `UPDATE ai_taste_spend SET micro_usd = GREATEST(micro_usd + $2, 0) WHERE day = $1`, DayStart(day), microUSD(deltaUSD))
	return err
}

// Today is the day's spend so far, in dollars.
func (sp *Spend) Today(ctx context.Context, now time.Time) (float64, error) {
	var m int64
	err := sp.db.QueryRowContext(ctx, `SELECT micro_usd FROM ai_taste_spend WHERE day = $1`, DayStart(now)).Scan(&m)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return float64(m) / micro, err
}

// Gate is one request's ai.Taste for a free account: the ceiling's hold first (so a refused call burns no taste), then the user's unit.
type Gate struct {
	Store *Store
	Spend *Spend
	User  uuid.UUID
	Quota int
	Now   func() time.Time

	mu    sync.Mutex
	taken bool
	day   time.Time
	held  float64 // what the day's spend carries for this call: the estimate, then the real cost
}

func (g *Gate) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

// Take implements ai.Taste. Every failure to ask a store refuses the call.
func (g *Gate) Take(ctx context.Context) error {
	now := g.now()
	ok, err := g.Spend.Hold(ctx, now, EstimateUSD)
	if err != nil {
		return fmt.Errorf("%w: %v", ai.ErrOperatorDenied, err)
	}
	if !ok {
		return ai.ErrTasteCeiling
	}
	got, err := g.Store.Reserve(ctx, g.User, now, g.Quota)
	if err != nil || !got {
		g.release(ctx, now, EstimateUSD)
		if err != nil {
			return fmt.Errorf("%w: %v", ai.ErrOperatorDenied, err)
		}
		return ai.ErrTasteUsed
	}
	g.mu.Lock()
	g.taken, g.day, g.held = true, now, EstimateUSD
	g.mu.Unlock()
	return nil
}

func (g *Gate) release(ctx context.Context, day time.Time, usd float64) {
	_ = g.Spend.Adjust(context.WithoutCancel(ctx), day, -usd)
}

// Refund implements ai.Taste.
func (g *Gate) Refund(ctx context.Context) {
	g.mu.Lock()
	taken, day, held := g.taken, g.day, g.held
	g.taken = false
	g.mu.Unlock()
	if !taken {
		return
	}
	_ = g.Store.Refund(ctx, g.User, day)
	g.release(ctx, day, held)
}

// Spent implements ai.Taste: the hold becomes the real cost.
func (g *Gate) Spent(ctx context.Context, usd float64) {
	g.mu.Lock()
	taken, day, held := g.taken, g.day, g.held
	g.held = usd
	g.mu.Unlock()
	if !taken {
		return
	}
	_ = g.Spend.Adjust(context.WithoutCancel(ctx), day, usd-held)
}
