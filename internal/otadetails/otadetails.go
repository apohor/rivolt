// Package otadetails keeps each vehicle's OTA release details (current and
// available software version, release-notes link) fresh from Rivian's
// getOTAUpdateDetails: once a day per vehicle and when a monitor connects,
// never per request. The API reads what is stored.
package otadetails

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
)

const (
	// minInterval is the gap between two fetches of one vehicle, across
	// pods and restarts (a little under a day so a daily sweep always finds
	// it due).
	minInterval = 23 * time.Hour
	// vehiclePace spaces the vehicles of one user; userPace the users of a
	// sweep.
	vehiclePace = 5 * time.Second
	userPace    = 20 * time.Second
	sweepEvery  = 6 * time.Hour
)

// store is the Postgres side; *PGStore implements it.
type store interface {
	Claim(ctx context.Context, uid uuid.UUID, rivianID string, minInterval time.Duration) (bool, error)
	Save(ctx context.Context, uid uuid.UUID, rivianID string, info *rivian.OTAInfo) error
	MarkUnavailable(ctx context.Context, uid uuid.UUID, rivianID string) error
	Vehicles(ctx context.Context, uid uuid.UUID) ([]string, error)
	// Seed records a vehicle's first-seen versions as the silent baseline
	// (true only the first time); ClaimNotice reports whether an alert for
	// the (kind, version) is new; Label names the vehicle for the alert.
	Seed(ctx context.Context, uid uuid.UUID, rivianID, installed, available string) (bool, error)
	ClaimNotice(ctx context.Context, uid uuid.UUID, rivianID, kind, version string) (bool, error)
	Label(ctx context.Context, uid uuid.UUID, rivianID string) string
}

// Update is a software-update event worth an alert: a new version offered
// (Installed false) or the installed version changed (Installed true).
type Update struct {
	UserID    uuid.UUID
	Vehicle   string // model or name, e.g. "R1S"
	Version   string
	Installed bool
}

// Notifier delivers an Update (web push). It must not block.
type Notifier func(Update)

// pgStore stores the details in vehicles.metadata.
type pgStore struct{ d *sql.DB }

func (p *pgStore) Claim(ctx context.Context, uid uuid.UUID, rivianID string, min time.Duration) (bool, error) {
	return db.ClaimOTAFetch(ctx, p.d, uid, rivianID, min)
}

func (p *pgStore) Save(ctx context.Context, uid uuid.UUID, rivianID string, info *rivian.OTAInfo) error {
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return db.SetVehicleOTA(ctx, p.d, uid, rivianID, raw)
}

func (p *pgStore) MarkUnavailable(ctx context.Context, uid uuid.UUID, rivianID string) error {
	return db.MarkOTAUnavailable(ctx, p.d, uid, rivianID)
}

func (p *pgStore) Seed(ctx context.Context, uid uuid.UUID, rivianID, installed, available string) (bool, error) {
	return db.SeedOTANotices(ctx, p.d, uid, rivianID, installed, available)
}

func (p *pgStore) ClaimNotice(ctx context.Context, uid uuid.UUID, rivianID, kind, version string) (bool, error) {
	return db.ClaimOTANotice(ctx, p.d, uid, rivianID, kind, version)
}

// Label is the vehicle's model ("R1S"), else its name, else "".
func (p *pgStore) Label(ctx context.Context, uid uuid.UUID, rivianID string) string {
	var model, name sql.NullString
	_ = p.d.QueryRowContext(ctx, `SELECT model, display_name FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2`,
		uid, rivianID).Scan(&model, &name)
	if model.Valid && model.String != "" {
		return model.String
	}
	return name.String
}

func (p *pgStore) Vehicles(ctx context.Context, uid uuid.UUID) ([]string, error) {
	return db.ListUserRivianVehicleIDs(ctx, p.d, uid)
}

// Syncer fetches OTA details for the vehicles of users with a Rivian
// session.
type Syncer struct {
	st       store
	accounts rivian.AccountRegistry
	users    func(ctx context.Context) ([]uuid.UUID, error)
	logger   *slog.Logger
	sleep    func(ctx context.Context, d time.Duration)
	notify   Notifier

	// fence, when set, says which vehicles' groups this pod owns (single-owner, lead mode).
	fence atomic.Pointer[vehicleFencer]
}

type vehicleFencer struct{ fence.Source }

// SetFence makes the sync fetch only the vehicles whose group this pod owns, each write carrying
// that lease (lead mode); with lead mode off the source says Off and nothing changes.
func (s *Syncer) SetFence(f fence.Source) {
	if f == nil {
		s.fence.Store(nil)
		return
	}
	s.fence.Store(&vehicleFencer{f})
}

// SetNotifier makes the syncer announce new and installed versions. Call
// before Run.
func (s *Syncer) SetNotifier(n Notifier) { s.notify = n }

// announce alerts on a version not announced before. A vehicle's first
// sync only records its baseline. Failures are logged, never fatal: the
// details are already saved and the next pass retries what was not
// claimed.
func (s *Syncer) announce(ctx context.Context, uid uuid.UUID, id string, info *rivian.OTAInfo) {
	if s.notify == nil || info == nil {
		return
	}
	installed, available := "", ""
	if info.Current != nil {
		installed = info.Current.Version
	}
	if info.Available != nil && info.Available.Version != installed {
		available = info.Available.Version
	}
	seeded, err := s.st.Seed(ctx, uid, id, installed, available)
	if err != nil {
		s.logger.Warn("ota details: baseline failed", "user_id", uid.String(), "err", err.Error())
		return
	}
	if seeded {
		return
	}
	label := s.st.Label(ctx, uid, id)
	for _, e := range []struct {
		kind, version string
		installed     bool
	}{
		{db.OTAAvailable, available, false},
		{db.OTAInstalled, installed, true},
	} {
		if e.version == "" {
			continue
		}
		fresh, err := s.st.ClaimNotice(ctx, uid, id, e.kind, e.version)
		if err != nil {
			s.logger.Warn("ota details: notice claim failed", "user_id", uid.String(), "err", err.Error())
			continue
		}
		if fresh {
			s.notify(Update{UserID: uid, Vehicle: label, Version: e.version, Installed: e.installed})
		}
	}
}

// New wires a Syncer over a pool.
func New(pool *sql.DB, accounts rivian.AccountRegistry, users func(context.Context) ([]uuid.UUID, error), logger *slog.Logger) *Syncer {
	return newSyncer(&pgStore{pool}, accounts, users, logger)
}

func newSyncer(st store, accounts rivian.AccountRegistry, users func(context.Context) ([]uuid.UUID, error), logger *slog.Logger) *Syncer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Syncer{st: st, accounts: accounts, users: users, logger: logger, sleep: sleepCtx}
}

// SyncUser fetches the OTA details of each of the user's vehicles that is
// due (no attempt in the last 23 hours), vehicles vehiclePace apart. It
// stops at the first upstream error: the rest wait for the next pass. It
// returns how many vehicles were fetched.
func (s *Syncer) SyncUser(ctx context.Context, uid uuid.UUID) (int, error) {
	ids, err := s.st.Vehicles(ctx, uid)
	if err != nil {
		return 0, err
	}
	// Single-owner: only the vehicles whose group this pod holds, each with its own token on the
	// context so every write for it carries the lease. Decided before the claim and before the
	// Rivian account is touched, so a pod that owns none of the user's vehicles does nothing.
	type owned struct {
		id  string
		ctx context.Context
	}
	var mine []owned
	for _, id := range ids {
		vctx := ctx
		if f := s.fence.Load(); f != nil {
			tok, st := f.Fence(id)
			switch st {
			case fence.Held:
				vctx = fence.With(ctx, tok)
			case fence.Warming, fence.Lost:
				continue
			}
		}
		mine = append(mine, owned{id, vctx})
	}
	if len(mine) == 0 {
		return 0, nil
	}
	src, ok := s.accounts.For(uid).(rivian.OTASource)
	if !ok || src == nil {
		return 0, nil
	}
	fetched := 0
	for _, o := range mine {
		id, ctx := o.id, o.ctx
		if ctx.Err() != nil {
			return fetched, ctx.Err()
		}
		won, err := s.st.Claim(ctx, uid, id, minInterval)
		if err != nil {
			return fetched, err
		}
		if !won {
			continue
		}
		if fetched > 0 {
			s.sleep(ctx, vehiclePace)
		}
		info, err := src.OTAUpdateDetails(ctx, id)
		if err != nil {
			if notAvailable(err) {
				// Rivian won't give this vehicle's OTA details (FORBIDDEN or
				// unsupported, e.g. some R2s): expected, not a failure. Back
				// it off for a week and go on with the user's other vehicles.
				if merr := s.st.MarkUnavailable(ctx, uid, id); merr != nil {
					return fetched, merr
				}
				s.logger.Info("ota details: not available for this vehicle, backing off a week",
					"user_id", uid.String(), "code", rivian.ErrCode(err), "err", describe(err))
				continue
			}
			return fetched, err
		}
		if err := s.st.Save(ctx, uid, id, info); err != nil {
			return fetched, err
		}
		s.announce(ctx, uid, id, info)
		fetched++
	}
	return fetched, nil
}

// OnConnect fetches for a user whose monitor just connected (boot or login),
// in the background. The claim keeps it to once a day per vehicle.
func (s *Syncer) OnConnect(uid uuid.UUID) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := s.SyncUser(ctx, uid); err != nil {
			s.logger.Warn("ota details: sync on connect failed", "user_id", uid.String(), "err", describe(err))
		}
	}()
}

// Run sweeps until ctx ends: every sweepEvery each user with a Rivian
// session, users userPace apart. Which vehicles are fetched is decided by
// the per-vehicle claim, so a sweep that finds nothing due costs no
// Rivian calls.
func (s *Syncer) Run(ctx context.Context) {
	s.sleep(ctx, time.Duration(rand.Int63n(int64(10*time.Minute))))
	for ctx.Err() == nil {
		uids, err := s.users(ctx)
		if err != nil {
			s.logger.Warn("ota details: list users failed", "err", err.Error())
		}
		rand.Shuffle(len(uids), func(i, j int) { uids[i], uids[j] = uids[j], uids[i] })
		for _, uid := range uids {
			if ctx.Err() != nil {
				return
			}
			n, err := s.SyncUser(ctx, uid)
			if err != nil {
				s.logger.Warn("ota details: sync failed", "user_id", uid.String(), "err", describe(err))
			}
			if n > 0 {
				s.sleep(ctx, userPace)
			}
		}
		s.sleep(ctx, sweepEvery)
	}
}

// describe logs an error as its classification only (see rivian.Describe).
func describe(err error) string { return rivian.Describe(err) }

// notAvailable is an answer that says "not for this vehicle": a FORBIDDEN or
// unsupported code, or any GraphQL-level refusal (the gateway answered, with
// no HTTP failure and no class we know). Network failures, rate limits and
// outages are not, and stop the pass for a retry on the next one.
func notAvailable(err error) bool {
	if rivian.IsNotAvailable(err) {
		return true
	}
	var ue *rivian.UpstreamError
	if !errors.As(err, &ue) || ue.HTTPStatus != 0 || ue.Class != rivian.ClassUnknown {
		return false
	}
	// A transport failure the classifier did not recognise looks the same
	// (status 0, unknown): it is not a refusal.
	var netErr net.Error
	var urlErr *url.Error
	return !errors.As(err, &netErr) && !errors.As(err, &urlErr)
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
