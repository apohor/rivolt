package receipts

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

// store is what the Syncer needs from Postgres; *Store implements it.
type store interface {
	ClaimSummaries(ctx context.Context, uid uuid.UUID, minInterval time.Duration) (bool, error)
	ClaimDetails(ctx context.Context, uid uuid.UUID, minInterval time.Duration) (bool, error)
	MarkResult(ctx context.Context, uid uuid.UUID, runErr error) error
	UpsertReceipts(ctx context.Context, uid uuid.UUID, in []rivian.SessionSummary) error
	Unmatched(ctx context.Context, uid uuid.UUID) ([]Receipt, error)
	CandidateCharges(ctx context.Context, uid uuid.UUID, from, to time.Time) ([]ChargeRef, error)
	Apply(ctx context.Context, uid uuid.UUID, pairs []Pair) error
	RefreshMatched(ctx context.Context, uid uuid.UUID) error
	NeedDetail(ctx context.Context, uid uuid.UUID, limit int) ([]string, error)
	SaveDetail(ctx context.Context, uid uuid.UUID, txn string, c *rivian.SessionCharger) error
}

// Pacing. Rivian is called per user, never in bursts: the summaries call is
// one request that returns the whole history (the first one per user is the
// backfill), and the per-session detail calls that follow are capped and
// spaced.
const (
	// closeDelay waits after a charge closes: a receipt is not there the
	// instant the session ends.
	closeDelay = 10 * time.Minute
	// closeMinInterval is the shortest gap between two summaries calls for
	// one user (a close can trigger one).
	closeMinInterval = 30 * time.Minute
	// dailyInterval is the cadence of the scheduled sweep.
	dailyInterval = 24 * time.Hour
	// detailMinInterval spaces details-only passes (the backfill tail).
	detailMinInterval = 30 * time.Minute
	// maxDetailPerRun caps detail calls in one pass; detailPace spaces them.
	maxDetailPerRun = 10
	detailPace      = 3 * time.Second
	// userPace spaces users within a sweep.
	userPace = 20 * time.Second
	// sweepEvery is how often the loop looks for work.
	sweepEvery = 30 * time.Minute
)

// Result is what one Sync did, for logs and tests.
type Result struct {
	Skipped     bool // claimed by another run within the interval
	Fetched     int  // summaries returned
	Matched     int  // new receipt-to-charge links
	Details     int  // detail calls that succeeded
	DetailsLeft bool
}

// Syncer pulls receipts for users whose flag is on.
type Syncer struct {
	st       store
	accounts rivian.AccountRegistry
	enabled  func(uid uuid.UUID) bool
	users    func(ctx context.Context) ([]uuid.UUID, error)
	logger   *slog.Logger

	// seams for tests
	sleep func(ctx context.Context, d time.Duration)
	after func(d time.Duration, f func()) stopper

	mu     sync.Mutex
	timers map[uuid.UUID]stopper
}

type stopper interface{ Stop() bool }

// NewSyncer wires a Syncer. enabled gates a user (the rivian_receipts
// flag); users lists the users with a Rivian session for the sweep.
func NewSyncer(st *Store, accounts rivian.AccountRegistry, enabled func(uuid.UUID) bool, users func(context.Context) ([]uuid.UUID, error), logger *slog.Logger) *Syncer {
	return newSyncer(st, accounts, enabled, users, logger)
}

func newSyncer(st store, accounts rivian.AccountRegistry, enabled func(uuid.UUID) bool, users func(context.Context) ([]uuid.UUID, error), logger *slog.Logger) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{
		st: st, accounts: accounts, enabled: enabled, users: users, logger: logger,
		sleep:  sleepCtx,
		after:  func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
		timers: map[uuid.UUID]stopper{},
	}
}

// Sync runs one pass for a user: unless detailsOnly, fetch the summaries,
// store them and match them to charges; then fetch up to maxDetailPerRun
// charger details for matched receipts. minInterval is the claim window.
func (s *Syncer) Sync(ctx context.Context, uid uuid.UUID, minInterval time.Duration, detailsOnly bool) (Result, error) {
	var res Result
	if s.enabled != nil && !s.enabled(uid) {
		res.Skipped = true
		return res, nil
	}
	acct := s.accounts.For(uid)
	src, ok := acct.(rivian.ReceiptSource)
	if !ok || src == nil {
		res.Skipped = true
		return res, nil
	}
	var claimed bool
	var err error
	if detailsOnly {
		claimed, err = s.st.ClaimDetails(ctx, uid, minInterval)
	} else {
		claimed, err = s.st.ClaimSummaries(ctx, uid, minInterval)
	}
	if err != nil {
		return res, err
	}
	if !claimed {
		res.Skipped = true
		return res, nil
	}
	runErr := s.run(ctx, uid, src, detailsOnly, &res)
	var recorded error
	if runErr != nil {
		recorded = errors.New(describe(runErr))
	}
	if merr := s.st.MarkResult(ctx, uid, recorded); merr != nil {
		s.logger.Warn("receipts: mark result failed", "user_id", uid.String(), "err", merr.Error())
	}
	return res, runErr
}

func (s *Syncer) run(ctx context.Context, uid uuid.UUID, src rivian.ReceiptSource, detailsOnly bool, res *Result) error {
	if !detailsOnly {
		sums, err := src.CompletedSessionSummaries(ctx)
		if err != nil {
			return err
		}
		res.Fetched = len(sums)
		if err := s.st.UpsertReceipts(ctx, uid, sums); err != nil {
			return err
		}
		n, err := s.match(ctx, uid)
		if err != nil {
			return err
		}
		res.Matched = n
		if err := s.st.RefreshMatched(ctx, uid); err != nil {
			return err
		}
	}
	txns, err := s.st.NeedDetail(ctx, uid, maxDetailPerRun+1)
	if err != nil {
		return err
	}
	if len(txns) > maxDetailPerRun {
		txns = txns[:maxDetailPerRun]
		res.DetailsLeft = true
	}
	for i, txn := range txns {
		if i > 0 {
			s.sleep(ctx, detailPace)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		ch, err := src.SessionCharger(ctx, txn)
		if err != nil {
			// Stop at the first failure (breaker, kill switch, rate limit):
			// the rest waits for the next pass.
			res.DetailsLeft = true
			return err
		}
		if err := s.st.SaveDetail(ctx, uid, txn, ch); err != nil {
			return err
		}
		res.Details++
	}
	return nil
}

func (s *Syncer) match(ctx context.Context, uid uuid.UUID) (int, error) {
	rs, err := s.st.Unmatched(ctx, uid)
	if err != nil || len(rs) == 0 {
		return 0, err
	}
	from, to := rs[0].StartedAt, rs[0].EndedAt
	for _, r := range rs {
		if r.StartedAt.Before(from) {
			from = r.StartedAt
		}
		if r.EndedAt.After(to) {
			to = r.EndedAt
		}
	}
	cs, err := s.st.CandidateCharges(ctx, uid, from.Add(-time.Hour), to.Add(time.Hour))
	if err != nil {
		return 0, err
	}
	pairs := Match(cs, rs)
	if err := s.st.Apply(ctx, uid, pairs); err != nil {
		return 0, err
	}
	return len(pairs), nil
}

// OnChargeClose schedules a sync for the user after closeDelay. One timer
// per user: further closes while it is pending are absorbed.
func (s *Syncer) OnChargeClose(uid uuid.UUID) {
	if s.enabled != nil && !s.enabled(uid) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, pending := s.timers[uid]; pending {
		return
	}
	s.timers[uid] = s.after(closeDelay, func() {
		s.mu.Lock()
		delete(s.timers, uid)
		s.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if _, err := s.Sync(ctx, uid, closeMinInterval, false); err != nil {
			s.logger.Warn("receipts: sync after charge close failed", "user_id", uid.String(), "err", describe(err))
		}
	})
}

// Run sweeps until ctx ends: every sweepEvery it syncs each enabled user
// once a day (summaries) and gives users with unfetched details a
// details-only pass, one user at a time, userPace apart.
func (s *Syncer) Run(ctx context.Context) {
	// Spread pods and restarts: first look somewhere in the first 5 minutes.
	s.sleep(ctx, time.Duration(rand.Int63n(int64(5*time.Minute))))
	for ctx.Err() == nil {
		s.sweep(ctx)
		s.sleep(ctx, sweepEvery)
	}
}

func (s *Syncer) sweep(ctx context.Context) {
	if s.users == nil {
		return
	}
	uids, err := s.users(ctx)
	if err != nil {
		s.logger.Warn("receipts: list users failed", "err", err.Error())
		return
	}
	rand.Shuffle(len(uids), func(i, j int) { uids[i], uids[j] = uids[j], uids[i] })
	for _, uid := range uids {
		if ctx.Err() != nil {
			return
		}
		if s.enabled != nil && !s.enabled(uid) {
			continue
		}
		res, err := s.Sync(ctx, uid, dailyInterval, false)
		if err == nil && res.Skipped {
			// Not due for summaries: finish a backfill tail if one is waiting.
			res, err = s.Sync(ctx, uid, detailMinInterval, true)
		}
		if err != nil {
			s.logger.Warn("receipts: sync failed", "user_id", uid.String(), "err", describe(err))
		} else if !res.Skipped {
			s.logger.Info("receipts: synced", "user_id", uid.String(),
				"fetched", res.Fetched, "matched", res.Matched, "details", res.Details)
		}
		if !res.Skipped {
			s.sleep(ctx, userPace)
		}
	}
}

// describe is how an error is logged and stored: classification only (see
// rivian.Describe), never a response slice.
func describe(err error) string { return rivian.Describe(err) }

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
