package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/flags"
	"github.com/apohor/rivolt/internal/idp"
)

// Inactive-account purge: an account that never connected a vehicle is warned
// by email at day 90 and deleted at day 120 after signup (owner rule,
// migration 0056). Only connecting a vehicle cancels it; signing in does not.
// Admins and disabled accounts are never touched. Off unless the inactive_account_purge
// flag says so: {"dry_run": true} logs counts only, {"users": [...]} acts on
// those accounts, {"all": true} on everyone eligible.

const (
	// purgeMaxWarnPerRun and purgeMaxDeletePerRun bound one daily run, so a
	// first activation over a backlog is spread over days instead of mailing
	// or deleting everyone at once.
	purgeMaxWarnPerRun   = 100
	purgeMaxDeletePerRun = 25
)

// purgeStore is the Postgres side; pgPurgeStore implements it.
type purgeStore interface {
	CancelWarnings(ctx context.Context) (int, error)
	WarnDue(ctx context.Context, limit int) ([]db.InactiveWarning, error)
	ClaimWarning(ctx context.Context, uid uuid.UUID) (bool, error)
	MarkWarned(ctx context.Context, uid uuid.UUID) error
	DeleteDue(ctx context.Context, limit int) ([]uuid.UUID, error)
	Delete(ctx context.Context, uid uuid.UUID) (username string, deleted bool, err error)
	Counts(ctx context.Context) (warnDue, deleteDue, warnedWaiting int, err error)
	// Accounts with no verified address to warn: deleted at day 120, no warning.
	UnwarnableDue(ctx context.Context, limit int) ([]uuid.UUID, error)
	DeleteUnwarnable(ctx context.Context, uid uuid.UUID) (username string, deleted bool, err error)
	UnwarnableCount(ctx context.Context) (int, error)
}

type pgPurgeStore struct{ d *sql.DB }

func (p pgPurgeStore) CancelWarnings(ctx context.Context) (int, error) {
	return db.CancelInactiveWarnings(ctx, p.d)
}
func (p pgPurgeStore) WarnDue(ctx context.Context, limit int) ([]db.InactiveWarning, error) {
	return db.ListInactiveWarnDue(ctx, p.d, limit)
}
func (p pgPurgeStore) ClaimWarning(ctx context.Context, uid uuid.UUID) (bool, error) {
	return db.ClaimInactiveWarning(ctx, p.d, uid)
}
func (p pgPurgeStore) MarkWarned(ctx context.Context, uid uuid.UUID) error {
	return db.MarkInactiveWarned(ctx, p.d, uid)
}
func (p pgPurgeStore) DeleteDue(ctx context.Context, limit int) ([]uuid.UUID, error) {
	return db.ListInactiveDeleteDue(ctx, p.d, limit)
}
func (p pgPurgeStore) Delete(ctx context.Context, uid uuid.UUID) (string, bool, error) {
	return db.DeleteInactiveAccount(ctx, p.d, uid)
}
func (p pgPurgeStore) Counts(ctx context.Context) (int, int, int, error) {
	return db.CountInactiveCandidates(ctx, p.d)
}
func (p pgPurgeStore) UnwarnableDue(ctx context.Context, limit int) ([]uuid.UUID, error) {
	return db.ListUnwarnableDeleteDue(ctx, p.d, limit)
}
func (p pgPurgeStore) DeleteUnwarnable(ctx context.Context, uid uuid.UUID) (string, bool, error) {
	return db.DeleteUnwarnableAccount(ctx, p.d, uid)
}
func (p pgPurgeStore) UnwarnableCount(ctx context.Context) (int, error) {
	return db.CountUnwarnableDue(ctx, p.d)
}

// mailSender is what the job needs from the mailer.
type mailSender interface {
	Send(ctx context.Context, m email.Message) error
}

// InactivePurge is the daily job.
type InactivePurge struct {
	store   purgeStore
	db      *sql.DB // for the IdP failure record
	flag    func() flags.InactivePurgeState
	mailer  mailSender
	idp     idp.UserProvider
	baseURL string
	logger  *slog.Logger
	sleep   func(ctx context.Context, d time.Duration)
}

// NewInactivePurge wires the job over the pool. flag reads the live
// inactive_account_purge flag; mailer may be nil (nothing is warned or
// deleted then: a deletion is never allowed without a sent warning).
func NewInactivePurge(pool *sql.DB, flag func() flags.InactivePurgeState, mailer *email.Client, users idp.UserProvider, baseURL string, logger *slog.Logger) *InactivePurge {
	j := newInactivePurge(pgPurgeStore{pool}, pool, flag, users, baseURL, logger)
	if mailer != nil {
		j.mailer = mailer
	}
	return j
}

func newInactivePurge(st purgeStore, pool *sql.DB, flag func() flags.InactivePurgeState, users idp.UserProvider, baseURL string, logger *slog.Logger) *InactivePurge {
	if logger == nil {
		logger = slog.Default()
	}
	return &InactivePurge{store: st, db: pool, flag: flag, idp: users, baseURL: baseURL, logger: logger, sleep: purgeSleep}
}

// PurgeResult is what one run did, for logs and tests.
type PurgeResult struct {
	Off       bool
	DryRun    bool
	Cancelled int
	Warned    int
	Deleted   int
	// UnwarnableDeleted counts accounts with no verified address deleted at day
	// 120 without a warning; UnwarnableDue is its dry-run count.
	UnwarnableDeleted int
	UnwarnableDue     int // dry run
	WarnDue           int // dry run
	DeleteDue         int // dry run
	WarnedWaiting     int // dry run
}

// RunOnce is one pass.
func (j *InactivePurge) RunOnce(ctx context.Context) PurgeResult {
	var res PurgeResult
	state := j.flag()
	if !state.DryRun && !state.All && len(state.Users) == 0 {
		res.Off = true
		return res
	}
	// Whichever mode, accounts that connected a vehicle since their warning
	// are released first.
	if n, err := j.store.CancelWarnings(ctx); err != nil {
		j.logger.Warn("inactive purge: cancel warnings failed", "err", describeSQL(err))
	} else {
		res.Cancelled = n
	}
	if state.DryRun {
		res.DryRun = true
		w, d, wait, err := j.store.Counts(ctx)
		if err != nil {
			j.logger.Warn("inactive purge: dry run count failed", "err", describeSQL(err))
			return res
		}
		res.WarnDue, res.DeleteDue, res.WarnedWaiting = w, d, wait
		if n, err := j.store.UnwarnableCount(ctx); err != nil {
			j.logger.Warn("inactive purge: dry run unwarnable count failed", "err", describeSQL(err))
		} else {
			res.UnwarnableDue = n
		}
		// Counts only: no ids, no emails.
		j.logger.Info("inactive purge dry run", "warn_due", w, "delete_due", d, "warned_waiting", wait, "unwarnable_due", res.UnwarnableDue, "cancelled", res.Cancelled)
		return res
	}
	// Accounts with no verified address to warn need no mailer: they get no mail.
	res.UnwarnableDeleted = j.deleteUnwarnable(ctx, state)
	if j.mailer == nil {
		j.logger.Warn("inactive purge: no mailer, nothing is warned or deleted")
		return res
	}
	res.Warned = j.warn(ctx, state)
	res.Deleted = j.delete(ctx, state)
	if res.Warned > 0 || res.Deleted > 0 || res.Cancelled > 0 || res.UnwarnableDeleted > 0 {
		j.logger.Info("inactive purge", "warned", res.Warned, "deleted", res.Deleted, "unwarnable_deleted", res.UnwarnableDeleted, "cancelled", res.Cancelled)
	}
	return res
}

// deleteUnwarnable deletes the never-connected accounts that have no verified
// address, at day 120, with no warning (there is nowhere to send one). Same flag
// scoping, per-run bound, row-lock re-check and identity-provider removal as the
// warned deletions.
func (j *InactivePurge) deleteUnwarnable(ctx context.Context, state flags.InactivePurgeState) int {
	due, err := j.store.UnwarnableDue(ctx, purgeMaxDeletePerRun*4)
	if err != nil {
		j.logger.Warn("inactive purge: list unwarnable failed", "err", describeSQL(err))
		return 0
	}
	deleted := 0
	for _, uid := range due {
		if deleted >= purgeMaxDeletePerRun || ctx.Err() != nil {
			break
		}
		if !state.Enabled(uid.String()) {
			continue
		}
		username, ok, err := j.store.DeleteUnwarnable(ctx, uid)
		if err != nil {
			j.logger.Warn("inactive purge: delete unwarnable failed", "user_id", uid.String(), "err", describeSQL(err))
			continue
		}
		if !ok {
			continue // connected, or verified an address, since; or already gone
		}
		deleted++
		status, _ := deleteFromIdP(ctx, j.db, j.idp, username, j.logger)
		j.logger.Info("inactive account deleted (no verified address to warn)", "user_id", uid.String(), "idp", status)
	}
	return deleted
}

func (j *InactivePurge) warn(ctx context.Context, state flags.InactivePurgeState) int {
	due, err := j.store.WarnDue(ctx, purgeMaxWarnPerRun*4)
	if err != nil {
		j.logger.Warn("inactive purge: list warnings failed", "err", describeSQL(err))
		return 0
	}
	sent := 0
	for _, w := range due {
		if sent >= purgeMaxWarnPerRun || ctx.Err() != nil {
			break
		}
		if !state.Enabled(w.UserID.String()) {
			continue
		}
		won, err := j.store.ClaimWarning(ctx, w.UserID)
		if err != nil {
			j.logger.Warn("inactive purge: claim failed", "user_id", w.UserID.String(), "err", describeSQL(err))
			continue
		}
		if !won {
			continue // another replica, or they just connected a vehicle
		}
		if err := j.sendWarning(ctx, w); err != nil {
			// Not recorded as warned: the lease expires and a later run
			// retries. An unsent warning must never lead to a deletion.
			j.logger.Warn("inactive purge: warning email not sent", "user_id", w.UserID.String(), "err", redactIdentifier(err.Error(), w.Email))
			continue
		}
		if err := j.store.MarkWarned(ctx, w.UserID); err != nil {
			j.logger.Error("inactive purge: warning sent but not recorded", "user_id", w.UserID.String(), "err", describeSQL(err))
			continue
		}
		sent++
	}
	return sent
}

func (j *InactivePurge) delete(ctx context.Context, state flags.InactivePurgeState) int {
	due, err := j.store.DeleteDue(ctx, purgeMaxDeletePerRun*4)
	if err != nil {
		j.logger.Warn("inactive purge: list deletions failed", "err", describeSQL(err))
		return 0
	}
	deleted := 0
	for _, uid := range due {
		if deleted >= purgeMaxDeletePerRun || ctx.Err() != nil {
			break
		}
		if !state.Enabled(uid.String()) {
			continue
		}
		username, ok, err := j.store.Delete(ctx, uid)
		if err != nil {
			j.logger.Warn("inactive purge: delete failed", "user_id", uid.String(), "err", describeSQL(err))
			continue
		}
		if !ok {
			continue // connected a vehicle since, or already gone
		}
		deleted++
		// The same IdP removal (and failure queue) as every account delete.
		status, _ := deleteFromIdP(ctx, j.db, j.idp, username, j.logger)
		j.logger.Info("inactive account deleted", "user_id", uid.String(), "idp", status)
	}
	return deleted
}

func (j *InactivePurge) sendWarning(ctx context.Context, w db.InactiveWarning) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	link := strings.TrimRight(j.baseURL, "/") + "/"
	if j.baseURL == "" {
		link = "your Rivolt account"
	}
	date := w.DeleteOn.UTC().Format("January 2, 2006")
	return j.mailer.Send(ctx, email.Message{
		To:             w.Email,
		Subject:        fmt.Sprintf("Your Rivolt account will be deleted on %s unless you connect your Rivian", date),
		IdempotencyKey: "inactive-warning-" + w.UserID.String(),
		Text: "Your Rivolt account has no Rivian connected, and has not been used for a while.\n\n" +
			"We'll delete your account on " + date + " unless you connect your Rivian\n" +
			"before then.\n\n" +
			"Connect it here:\n\n  " + link + "\n\n" +
			"Connecting takes about two minutes: you sign in with the same login you use in\n" +
			"the Rivian app. Rivolt only reads your vehicle data and never sends commands to\n" +
			"your car. If you no longer want the account, there is nothing to do: it will be\n" +
			"removed automatically.\n",
	})
}

// Run sweeps once a day until ctx ends, starting after a delay so a restart
// does not race boot, with jitter so replicas don't tick together. Every send
// and delete is claim- or lock-gated, so it is safe on every replica.
func (j *InactivePurge) Run(ctx context.Context) {
	j.sleep(ctx, 3*time.Minute+time.Duration(rand.Int63n(int64(2*time.Minute))))
	for ctx.Err() == nil {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		j.RunOnce(rctx)
		cancel()
		j.sleep(ctx, 24*time.Hour)
	}
}

// describeSQL keeps database error text out of logs beyond the driver's
// own message; it carries no user data of ours, but strip any value echo.
func describeSQL(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}

func purgeSleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
