package rivian

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// LiveStatePurger removes every live-state key of a user from Redis (and the planner quota counters, which carry the user id in their names): the in-flight drive and
// charge accumulators (<prefix>:livestate:<uid>:<vehicle>, which carry coordinates and a path,
// up to LiveStateTTL) and the published state (…:state). Account deletion calls it so nothing
// of the user outlives the account in Redis (privacy.md lists only logs and backups as copies
// kept after deletion).
//
// It uses SCAN with MATCH on the user's own key prefix, in small batches, never KEYS. A monitor
// that is still running on another pod can write the keys once more in the moments before it
// notices the deletion and stops, so Purge runs a second pass after Again.
type LiveStatePurger struct {
	Rdb    *redis.Client
	Prefix string
	// Again is the delay of the second pass (default 10 s).
	Again time.Duration
	Log   *slog.Logger
}

const purgeScanCount = 200

// patterns are the user's keys in Redis: the live-state snapshots (<prefix>:livestate:<uid>:…) and
// the planner quota counters (<prefix>:planquota:<uid>:<hour>, a count per hour that carries the
// user id in its name). The planner's leg cache is not here: it is keyed by a hash with no user id,
// so it cannot be attributed to a user and expires in 15 minutes.
func (p *LiveStatePurger) patterns(uid string) []string {
	prefix := p.Prefix
	if prefix == "" {
		prefix = "rivolt"
	}
	// The user id is a UUID: it carries no glob characters.
	return []string{
		fmt.Sprintf("%s:livestate:%s:*", prefix, uid),
		fmt.Sprintf("%s:planquota:%s:*", prefix, uid),
	}
}

// PurgeNow deletes the user's live-state keys now and returns how many it removed.
func (p *LiveStatePurger) PurgeNow(ctx context.Context, uid string) (int, error) {
	if p == nil || p.Rdb == nil || uid == "" {
		return 0, nil
	}
	removed := 0
	for _, pattern := range p.patterns(uid) {
		var cursor uint64
		for {
			keys, next, err := p.Rdb.Scan(ctx, cursor, pattern, purgeScanCount).Result()
			if err != nil {
				return removed, fmt.Errorf("scan user keys: %w", err)
			}
			if len(keys) > 0 {
				n, err := p.Rdb.Del(ctx, keys...).Result()
				if err != nil {
					return removed, fmt.Errorf("delete user keys: %w", err)
				}
				removed += int(n)
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	return removed, nil
}

// Purge deletes the user's live-state keys now (best effort: a failure is logged, never fatal
// to the deletion) and again after Again, for a write that was in flight.
func (p *LiveStatePurger) Purge(ctx context.Context, uid string) {
	if p == nil || p.Rdb == nil {
		return
	}
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	run := func(ctx context.Context) {
		if _, err := p.PurgeNow(ctx, uid); err != nil {
			log.Warn("live state purge failed", "err", err.Error())
		}
	}
	first, cancel := context.WithTimeout(ctx, 10*time.Second)
	run(first)
	cancel()
	again := p.Again
	if again <= 0 {
		again = 10 * time.Second
	}
	time.AfterFunc(again, func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		run(c)
	})
}
