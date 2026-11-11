// Package vehiclediscovery finds the linked users nobody records, and primes their vehicles (single-owner M3-6).
//
// A user's vehicles are leased to a pod by group; a user with a stored Rivian session but no vehicle row has no group, so no lease and no
// owner, and nothing records them. The sign-in's vehicle prime creates the rows, and /api/vehicles retries it when the user opens the app, but a
// user who does not open the app would be silently never recorded. (The boot-time refresh that used to find such users on every pod is gone.)
// So one pod, every Interval, looks up the vehicles of each such user and primes the rows; the lease coordinator then picks them up.
//
// One pod runs it (a Postgres advisory lock). Per user, a backoff held in Redis under the install's key prefix, named by an HMAC of the user id,
// bounds the Rivian calls: an error is retried after ErrorBackoff, an empty result (an account with no vehicle yet, e.g. a reservation) after
// EmptyBackoff, so a vehicle-less account does not cost a Rivian call every half hour for ever. A lookup that says the Rivian session is invalid
// goes through the client's own reauth path (the flag and the one-time email) and the user is not looked up again until they sign in: the
// candidate query leaves flagged users out.
package vehiclediscovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/singleton"
)

const (
	Interval     = 30 * time.Minute
	ErrorBackoff = 30 * time.Minute
	EmptyBackoff = 24 * time.Hour
	// perTick bounds one pass; Pace spaces the lookups so a backlog is not a burst against Rivian.
	perTick = 20
	Pace    = 3 * time.Second
	jobName = "vehicle-discovery"
)

// Outcomes of one user's turn, the label of the counter.
const (
	OutcomeStored      = "stored"
	OutcomeEmpty       = "empty"
	OutcomeError       = "error"
	OutcomeSkipped     = "skipped"
	OutcomeNeedsReauth = "needs_reauth"
)

// Lookup asks Rivian for the user's vehicles with the user's own session (the caller restores it from the secrets store if this pod's client has
// none). It is the only Rivian call the job makes.
type Lookup func(ctx context.Context, uid uuid.UUID) ([]rivian.Vehicle, error)

// Backoff remembers, per user, until when not to look up again.
type Backoff interface {
	Held(ctx context.Context, uid uuid.UUID) bool
	Hold(ctx context.Context, uid uuid.UUID, d time.Duration)
}

type Job struct {
	DB      *sql.DB
	Lookup  Lookup
	Backoff Backoff
	Observe func(outcome string)
	Logger  *slog.Logger
	Sleep   func(ctx context.Context, d time.Duration)

	// Owner mode (single-owner M4-2b, RIVOLT_OWNER_DISCOVERY): Users returns the users of the groups this pod owns. The job then runs on every
	// pod for its own users, takes no advisory lock, and keeps what it found in vehicle_discovery (so the API can tell "looking" from "none")
	// instead of a Redis backoff. Nil: the original single-pod job.
	Users func(ctx context.Context) ([]OwnedUser, error)
	// nudge asks the owner job to look one user up now (the sign-in's adopt nudge). Made by OwnerMode, before Run starts and NudgeUser is called.
	nudge chan uuid.UUID
	// lastForced is when each user was last looked up by a sign-in nudge (only the Run goroutine touches it).
	lastForced map[uuid.UUID]time.Time
}

// OwnerMode switches the job to owner mode with users as the pod's own users and creates the nudge queue. Call it before Run and before the job
// is shared with NudgeUser; nothing after that writes the queue.
func (j *Job) OwnerMode(users func(ctx context.Context) ([]OwnedUser, error)) {
	j.Users, j.Backoff = users, nil
	j.nudge = make(chan uuid.UUID, 16)
}

// ForceEvery is the shortest time between two sign-in lookups of the same user: a fresh sign-in looks up again at once whatever the 'none' or
// 'error' backoff says (the user may have just got the truck), but a user who signs in over and over costs Rivian one call per ForceEvery.
const ForceEvery = 10 * time.Minute

// OwnedUser is a user this pod owns, with the fence token of their group: the job writes its answer under it, so a pod that has lost the group
// writes nothing.
type OwnedUser struct {
	UID   uuid.UUID
	Token fence.Token
}

// OwnerInterval is how often the owner job looks for users that are due; each user's own next_check_at decides whether they are looked up.
const OwnerInterval = 5 * time.Minute

// NudgeUser asks the owner job to look this user up at once (best effort: a full queue drops it, the next tick finds the user anyway).
func (j *Job) NudgeUser(uid uuid.UUID) {
	if j == nil || j.nudge == nil {
		return
	}
	select {
	case j.nudge <- uid:
	default:
	}
}

func (j *Job) observe(o string) {
	if j.Observe != nil {
		j.Observe(o)
	}
}

func (j *Job) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// Run ticks every Interval until ctx ends (owner mode: every OwnerInterval, and when a user is nudged).
func (j *Job) Run(ctx context.Context) {
	if j == nil || j.DB == nil || j.Lookup == nil {
		return
	}
	if j.Users != nil {
		j.runOwner(ctx)
		return
	}
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			j.Tick(ctx)
		}
	}
}

// Tick is one pass, under the cross-replica lock: a pod that does not get it does nothing.
func (j *Job) Tick(ctx context.Context) {
	if _, err := singleton.Run(ctx, j.DB, jobName, singleton.Key{Name: "rivolt:" + jobName}, j.pass); err != nil {
		j.logger().Warn("vehicle discovery pass failed", "err", err.Error())
	}
}

func (j *Job) runOwner(ctx context.Context) {
	t := time.NewTicker(OwnerInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := j.ownerPass(ctx); err != nil {
				j.logger().Warn("vehicle discovery (owner) pass failed", "err", err.Error())
			}
		case uid := <-j.nudge:
			j.signedIn(ctx, uid)
		}
	}
}

// ownerPass looks up the due users among the groups this pod owns.
func (j *Job) ownerPass(ctx context.Context) error {
	mine, err := j.Users(ctx)
	if err != nil {
		return err
	}
	tok := make(map[uuid.UUID]fence.Token, len(mine))
	ids := make([]uuid.UUID, len(mine))
	for i, m := range mine {
		tok[m.UID], ids[i] = m.Token, m.UID
	}
	uids, err := db.ListLinkedUsersWithoutVehiclesAmong(ctx, j.DB, ids, perTick)
	if err != nil {
		return err
	}
	for i, uid := range uids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if i > 0 {
			j.sleep(ctx, Pace)
		}
		j.observe(j.one(withToken(ctx, tok[uid]), uid))
	}
	return nil
}

// signedIn is a sign-in nudge: if this pod owns the user and at most one such lookup ran in the last ForceEvery, the stored 'none' or 'error'
// is dropped first so the user is due, then they are looked up. A lease adopt or a roll move never comes here (only the post-login nudge does).
func (j *Job) signedIn(ctx context.Context, uid uuid.UUID) {
	if at, ok := j.lastForced[uid]; ok && time.Since(at) < ForceEvery {
		return
	}
	// Entries older than ForceEvery no longer suppress anything; drop them so the map holds only recent sign-ins.
	for id, at := range j.lastForced {
		if time.Since(at) >= ForceEvery {
			delete(j.lastForced, id)
		}
	}
	mine, err := j.Users(ctx)
	if err != nil {
		return
	}
	for _, m := range mine {
		if m.UID != uid {
			continue
		}
		if j.lastForced == nil {
			j.lastForced = map[uuid.UUID]time.Time{}
		}
		j.lastForced[uid] = time.Now()
		if err := db.ClearDiscovery(withToken(ctx, m.Token), j.DB, uid); err != nil {
			j.logger().Warn("vehicle discovery: clearing the stored answer at sign-in failed", "err", err.Error())
		}
		j.nudged(ctx, uid)
		return
	}
}

// SignedIn is signedIn for a caller that runs it itself (tests).
func (j *Job) SignedIn(ctx context.Context, uid uuid.UUID) { j.signedIn(ctx, uid) }

// nudged looks a user up now if this pod owns them and they are due.
func (j *Job) nudged(ctx context.Context, uid uuid.UUID) {
	mine, err := j.Users(ctx)
	if err != nil {
		return
	}
	for _, m := range mine {
		if m.UID != uid {
			continue
		}
		due, err := db.ListLinkedUsersWithoutVehiclesAmong(ctx, j.DB, []uuid.UUID{uid}, 1)
		if err == nil && len(due) == 1 {
			j.observe(j.one(withToken(ctx, m.Token), uid))
		}
		return
	}
}

func (j *Job) pass(ctx context.Context) error {
	uids, err := db.ListLinkedUsersWithoutVehicles(ctx, j.DB, perTick)
	if err != nil {
		return err
	}
	for i, uid := range uids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if j.Backoff != nil && j.Backoff.Held(ctx, uid) {
			j.observe(OutcomeSkipped)
			continue
		}
		if i > 0 {
			j.sleep(ctx, Pace)
		}
		j.observe(j.one(ctx, uid))
	}
	return nil
}

func (j *Job) sleep(ctx context.Context, d time.Duration) {
	if j.Sleep != nil {
		j.Sleep(ctx, d)
		return
	}
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

func (j *Job) hold(ctx context.Context, uid uuid.UUID, d time.Duration) {
	if j.Backoff != nil {
		j.Backoff.Hold(ctx, uid, d)
	}
	if j.Users != nil { // owner mode: the answer is kept in Postgres, where the API reads it and the next pass finds when to look again
		state := db.DiscoveryError
		if d >= EmptyBackoff {
			state = db.DiscoveryNone
		}
		if err := db.RecordDiscovery(ctx, j.DB, uid, state, time.Now().Add(d)); err != nil {
			j.logger().Warn("vehicle discovery: could not record the answer", "err", err.Error())
		}
	}
}

// one looks up and primes one user and returns the outcome. Nothing it logs names the user.
func (j *Job) one(ctx context.Context, uid uuid.UUID) string {
	lctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	vs, err := j.Lookup(lctx, uid)
	cancel()
	switch {
	case errors.Is(err, rivian.ErrNeedsReauth), rivian.IsUpstream(err, rivian.ClassUserAction):
		// The client has flagged the account (and sent the one-time email) through its reauth sink. Held briefly in case the flag did not
		// reach the row, so the same user is not asked again at once; the candidate query leaves flagged users out.
		j.hold(ctx, uid, ErrorBackoff)
		return OutcomeNeedsReauth
	case errors.Is(err, rivian.ErrNotImplemented):
		j.hold(ctx, uid, EmptyBackoff)
		return OutcomeEmpty
	case err != nil:
		j.hold(ctx, uid, ErrorBackoff)
		j.logger().Warn("vehicle discovery lookup failed", "err", rivian.Describe(err))
		return OutcomeError
	}
	stored := 0
	for _, v := range vs {
		if v.ID == "" {
			continue
		}
		uerr := db.UpsertVehicleFromSync(ctx, j.DB, uid, db.VehicleSync{
			RivianID: v.ID, VIN: v.VIN, Name: v.Name, Model: v.Model, ModelYear: v.ModelYear,
			Trim: v.TrimID, PackKWh: v.PackKWh, ImageURL: v.ImageURL, Info: v.StoredInfo(),
		})
		if uerr != nil {
			j.logger().Warn("vehicle discovery prime failed", "err", uerr.Error())
			continue
		}
		stored++
	}
	switch {
	case stored > 0:
		if j.Users != nil {
			// found: nothing left to wait for. A failed clear leaves a stale row that the API reads below the vehicles, so it is logged, not fatal.
			if err := db.ClearDiscovery(ctx, j.DB, uid); err != nil {
				j.logger().Warn("vehicle discovery: clearing the stored answer failed", "err", err.Error())
			}
		}
		return OutcomeStored // the rows now exist: the user leaves the candidate set and the lease coordinator takes the vehicles
	case len(vs) == 0:
		j.hold(ctx, uid, EmptyBackoff)
		return OutcomeEmpty
	}
	// The lookup answered with vehicles, but none could be stored (no usable id, or every upsert failed and said so above): say how many, never who.
	j.logger().Warn("vehicle discovery stored nothing", "returned", len(vs), "with_id", withID(vs))
	j.hold(ctx, uid, ErrorBackoff)
	return OutcomeError
}

func withID(vs []rivian.Vehicle) int {
	n := 0
	for _, v := range vs {
		if v.ID != "" {
			n++
		}
	}
	return n
}

// ---- backoff stores

// RedisBackoff keeps the backoff in Redis under prefix, named by an HMAC of the user id (a user id never reaches Redis in clear), with the
// backoff as the TTL. A pod without Redis (or when Redis errors) keeps it in memory; only one pod runs the job at a time, so memory loses at
// most one extra attempt per handover.
type RedisBackoff struct {
	rdb    *redis.Client
	prefix string
	key    []byte
	now    func() time.Time

	mu    sync.Mutex
	local map[uuid.UUID]time.Time
	down  atomic.Int64
}

// BackoffKey derives the HMAC key from the limiters' key (a purpose-labelled subkey, as FailLimiterKey does from the server's secret). nil stays nil.
func BackoffKey(limiterKey []byte) []byte {
	if len(limiterKey) == 0 {
		return nil
	}
	m := hmac.New(sha256.New, limiterKey)
	m.Write([]byte("rivolt/vehicle-discovery/redis-keys/v1"))
	return m.Sum(nil)
}

func NewRedisBackoff(rdb *redis.Client, prefix string, limiterKey []byte) *RedisBackoff {
	return &RedisBackoff{rdb: rdb, prefix: prefix, key: BackoffKey(limiterKey), now: time.Now, local: map[uuid.UUID]time.Time{}}
}

func (b *RedisBackoff) redisKey(uid uuid.UUID) string {
	m := hmac.New(sha256.New, b.key)
	m.Write([]byte("vdisc|" + uid.String()))
	return b.prefix + ":vdisc:" + hex.EncodeToString(m.Sum(nil))
}

func (b *RedisBackoff) usable() bool {
	return b.rdb != nil && len(b.key) > 0 && b.now().UnixNano() >= b.down.Load()
}

func (b *RedisBackoff) Held(ctx context.Context, uid uuid.UUID) bool {
	b.mu.Lock()
	until, ok := b.local[uid]
	b.mu.Unlock()
	if ok && b.now().Before(until) {
		return true
	}
	if !b.usable() {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	n, err := b.rdb.Exists(cctx, b.redisKey(uid)).Result()
	if err != nil {
		b.down.Store(b.now().Add(15 * time.Second).UnixNano())
		return false
	}
	return n > 0
}

func (b *RedisBackoff) Hold(ctx context.Context, uid uuid.UUID, d time.Duration) {
	b.mu.Lock()
	b.local[uid] = b.now().Add(d)
	b.mu.Unlock()
	if !b.usable() {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := b.rdb.Set(cctx, b.redisKey(uid), "1", d).Err(); err != nil {
		b.down.Store(b.now().Add(15 * time.Second).UnixNano())
	}
}

// OwnerTick is one owner-mode pass now (tests, and a caller that wants a pass without waiting for the ticker).
func (j *Job) OwnerTick(ctx context.Context) error { return j.ownerPass(ctx) }

// LookUpNow looks one user up at once if this pod owns them and they are due (the adopt nudge, synchronously).
func (j *Job) LookUpNow(ctx context.Context, uid uuid.UUID) { j.nudged(ctx, uid) }

// withToken puts the group's fence token on ctx; a zero token (lead mode off) leaves the writes unguarded, as they were.
func withToken(ctx context.Context, t fence.Token) context.Context {
	if t.Group == "" {
		return ctx
	}
	return fence.With(ctx, t)
}
