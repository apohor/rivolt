// Package planleg caches planner calls and caps how many a user can make.
//
// A multi-day plan is one planner call per day, each ~10 s upstream, and the
// planner is edited a day at a time: re-planning after a change to day 3
// should cost day 3 (and later days whose start SoC moved), not every day.
// Cache wraps a rivian.TripPlanner so that
//
//   - a call with the same inputs inside 15 minutes is answered from Redis and
//     is free, and
//   - every call that does reach the upstream planner counts against the
//     user's 60 per hour; the 61st is refused with a QuotaError.
//
// Every planner is wrapped, not only the live one: a replay install's synthetic
// plan is a Valhalla route and a charger-archive scan, which a user can also
// run up. Simulated plans are deterministic for their inputs, so they cache
// like any other. Redis being down never blocks planning: the cache then
// misses and the quota allows.
package planleg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/rivian"
)

const (
	// TTL is how long a planned leg is served from cache.
	TTL = 15 * time.Minute
	// QuotaPerHour is the planner calls a user may send upstream per UTC hour.
	QuotaPerHour = 60
	// departureBucket is the resolution of the departure time in the cache key.
	departureBucket = 15 * time.Minute
	// opTimeout bounds each Redis call: a dead Redis must cost a planner call
	// a fraction of a second, not the client's connect timeout.
	opTimeout = 300 * time.Millisecond
	// backoff is how long Redis is left alone after it fails.
	backoff = 30 * time.Second
)

// Cache is the Redis-backed leg cache and quota. A nil *Cache wraps nothing.
type Cache struct {
	rdb    *redis.Client
	prefix string
	now    func() time.Time
	log    *slog.Logger
	// downUntil (unix nanos): after a Redis failure the cache is skipped until
	// then, so a dead Redis costs one slow call, not one per planner call.
	downUntil atomic.Int64
}

// New returns a Cache over rdb, or nil when there is no Redis. prefix keeps
// installs sharing a Redis out of each other's keys (RIVOLT_REDIS_KEY_PREFIX).
func New(rdb *redis.Client, prefix string, log *slog.Logger) *Cache {
	if rdb == nil {
		return nil
	}
	return &Cache{rdb: rdb, prefix: prefix, now: time.Now, log: log}
}

// QuotaError is returned when a user is over their hourly planner quota.
type QuotaError struct {
	// RetryAfter is the time until the quota window resets.
	RetryAfter time.Duration
}

func (e *QuotaError) Error() string {
	return fmt.Sprintf("trip planning limit reached (%d per hour); try again in %d minutes", QuotaPerHour, int(math.Ceil(e.RetryAfter.Minutes())))
}

// IsQuota reports whether err is a QuotaError and returns it.
func IsQuota(err error) (*QuotaError, bool) {
	var q *QuotaError
	if errors.As(err, &q) {
		return q, true
	}
	return nil, false
}

// Wrap returns inner behind the cache and the quota for uid. On a nil Cache it
// returns inner unchanged.
func (c *Cache) Wrap(inner rivian.TripPlanner, uid uuid.UUID) rivian.TripPlanner {
	if c == nil {
		return inner
	}
	return &cached{c: c, inner: inner, uid: uid}
}

type cached struct {
	c     *Cache
	inner rivian.TripPlanner
	uid   uuid.UUID
}

func (w *cached) PlanTrip(ctx context.Context, in rivian.PlanTripInput) (*rivian.TripPlan, error) {
	c := w.c
	now := c.now()
	key := c.legKey(w.uid, in, now)
	if plan, ok := c.get(ctx, key); ok {
		return plan, nil
	}
	if retry, ok := c.allow(ctx, w.uid, now); !ok {
		return nil, &QuotaError{RetryAfter: retry}
	}
	plan, err := w.inner.PlanTrip(ctx, in)
	if err != nil {
		return nil, err
	}
	if plan != nil {
		c.set(ctx, key, plan)
	}
	return plan, nil
}

// legKey is planleg:v1:<sha256 of the normalised inputs>. It covers the user,
// vehicle, starting SoC rounded to 1 %, every waypoint rounded to 1e-4 degrees,
// drive mode, trailer, route preferences, adapter, network preferences, the
// arrival target, and
// the departure rounded to 15 minutes (a departure that is not in the future
// means "now", so it is the current 15-minute slot).
func (c *Cache) legKey(uid uuid.UUID, in rivian.PlanTripInput, now time.Time) string {
	dep := in.DepartureTime
	if dep.IsZero() || !dep.After(now) {
		dep = now
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "uid=%s|veh=%s|soc=%d|", uid, in.VehicleID, int(math.Round(in.StartingSoC)))
	for _, w := range in.Waypoints {
		fmt.Fprintf(&sb, "wp=%.4f,%.4f,%s|", w.Latitude, w.Longitude, w.EntityID)
	}
	fmt.Fprintf(&sb, "drive=%s|trailer=%s|", in.DriveMode, in.TrailerProfile)
	if rp := in.RoutePreferences; rp.Any() {
		fmt.Fprintf(&sb, "routepref=%t,%t,%t|", rp.AvoidTolls, rp.AvoidFerries, rp.AvoidHighways)
	}
	switch {
	case in.HasAdapter == nil:
		sb.WriteString("adapter=unset|")
	case *in.HasAdapter:
		sb.WriteString("adapter=true|")
	default:
		sb.WriteString("adapter=false|")
	}
	if in.TargetArrivalSocPercent != nil {
		fmt.Fprintf(&sb, "target=%.1f|", *in.TargetArrivalSocPercent)
	} else {
		sb.WriteString("target=unset|")
	}
	ids := make([]string, 0, len(in.NetworkPreferences))
	for _, np := range in.NetworkPreferences {
		ids = append(ids, fmt.Sprintf("%s:%d", np.NetworkID, np.Preference))
	}
	sort.Strings(ids)
	fmt.Fprintf(&sb, "nets=%s|", strings.Join(ids, ","))
	fmt.Fprintf(&sb, "dep=%d", dep.UTC().Truncate(departureBucket).Unix())
	sum := sha256.Sum256([]byte(sb.String()))
	return c.prefix + ":planleg:v1:" + hex.EncodeToString(sum[:])
}

func (c *Cache) get(ctx context.Context, key string) (*rivian.TripPlan, bool) {
	if !c.usable() {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			c.warn("leg cache read failed", err)
		}
		return nil, false
	}
	var plan rivian.TripPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, false
	}
	return &plan, true
}

func (c *Cache) set(ctx context.Context, key string, plan *rivian.TripPlan) {
	if !c.usable() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	raw, err := json.Marshal(plan)
	if err != nil {
		return
	}
	if err := c.rdb.Set(ctx, key, raw, TTL).Err(); err != nil {
		c.warn("leg cache write failed", err)
	}
}

// allow counts one upstream call for uid in the current UTC hour. It reports
// false (with the time until the hour rolls over) when that is over the quota.
// Redis errors allow the call.
func (c *Cache) allow(ctx context.Context, uid uuid.UUID, now time.Time) (time.Duration, bool) {
	if !c.usable() {
		return 0, true
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	hour := now.UTC().Truncate(time.Hour)
	key := fmt.Sprintf("%s:planquota:%s:%s", c.prefix, uid, hour.Format("2006010215"))
	n, err := c.rdb.Incr(ctx, key).Result()
	if err != nil {
		c.warn("plan quota unavailable", err)
		return 0, true
	}
	if n == 1 {
		// An hour bucket is dead after the hour; keep the key a little longer
		// than that so a clock edge cannot resurrect a count.
		_ = c.rdb.Expire(ctx, key, 2*time.Hour).Err()
	}
	if n > QuotaPerHour {
		return hour.Add(time.Hour).Sub(now), false
	}
	return 0, true
}

// usable reports whether Redis may be tried now.
func (c *Cache) usable() bool { return time.Now().UnixNano() >= c.downUntil.Load() }

func (c *Cache) warn(msg string, err error) {
	c.downUntil.Store(time.Now().Add(backoff).UnixNano())
	if c.log != nil {
		c.log.Warn("planleg: "+msg, "err", err.Error())
	}
}
