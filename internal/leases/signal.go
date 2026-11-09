package leases

import (
	"context"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// A released-group signal (zero-impact deploys, G1a). A pod that gives groups up tells its peers through Redis, and each peer reconciles at
// once instead of waiting for its next tick (0 to 6 s). The tick stays the fallback: a lost message, a Redis outage or a peer that was
// not listening cost nothing but the old wait. The message carries no group ids and no state; it only says "look now", so a stale or
// duplicated one is harmless (the reconcile decides everything from Postgres).

const (
	// signalMinGap is the least time between two published signals from one pod: a burst of releases (a rebalance pass) is one message.
	signalMinGap = 500 * time.Millisecond
	// signalJitter spreads the peers' reconciles over this long after a message, so N pods do not all hit Postgres together.
	signalJitter = 300 * time.Millisecond
	// signalOp bounds the publish; a slow Redis must not slow a drain.
	signalOp = 300 * time.Millisecond
)

// ReleaseSignal carries "groups were released" between pods.
type ReleaseSignal interface {
	// Publish tells the other pods. Best effort.
	Publish(ctx context.Context) error
	// Subscribe calls fn when another pod has published, until ctx ends. A Redis error just skips that look.
	Subscribe(ctx context.Context, fn func())
}

// RedisSignal is the ReleaseSignal on one Redis counter under the install's key prefix: a release bumps it, every pod reads it once a second
// and reconciles when it moved. It is a counter and not pub/sub on purpose: the `rivolt` Valkey user may not use channels (resetchannels, no
// +@pubsub), a subscription would hold a connection per pod, and one GET a second per pod is nothing at 20 pods. The key expires, so it never
// lingers; a pod that starts later reads the current value as its baseline and reconciles at boot anyway.
type RedisSignal struct {
	rdb  *redis.Client
	key  string
	mine atomic.Int64 // the value this pod's own last bump produced, so it does not wake itself
}

// signalPoll is how often a pod looks at the counter: the average wait after a release is half of it, plus the jitter.
const signalPoll = time.Second

// signalKeyTTL: a counter nobody bumps for this long goes away.
const signalKeyTTL = 10 * time.Minute

// NewRedisSignal builds the signal.
func NewRedisSignal(rdb *redis.Client, prefix string) *RedisSignal {
	return &RedisSignal{rdb: rdb, key: prefix + ":leases:released"}
}

func (s *RedisSignal) Publish(ctx context.Context) error {
	c, cancel := context.WithTimeout(context.WithoutCancel(ctx), signalOp)
	defer cancel()
	p := s.rdb.Pipeline()
	incr := p.Incr(c, s.key)
	p.Expire(c, s.key, signalKeyTTL)
	if _, err := p.Exec(c); err != nil {
		return err
	}
	s.mine.Store(incr.Val())
	return nil
}

func (s *RedisSignal) Subscribe(ctx context.Context, fn func()) {
	last := int64(-1)
	t := time.NewTicker(signalPoll)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, signalOp)
		v, err := s.rdb.Get(c, s.key).Int64()
		cancel()
		if err == redis.Nil {
			v, err = 0, nil
		}
		if err == nil {
			if last >= 0 && v != last && v != s.mine.Load() {
				fn()
			}
			last = v
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// SetReleaseSignal wires the signal. Safe to call before Run; nil turns it off.
func (c *Coordinator) SetReleaseSignal(s ReleaseSignal) { c.signal = s }

// signalReleased tells the peers that this pod released groups. Rate-limited to one message per signalMinGap.
func (c *Coordinator) signalReleased(ctx context.Context) {
	if c.signal == nil {
		return
	}
	c.sigMu.Lock()
	if time.Since(c.sigAt) < signalMinGap {
		c.sigMu.Unlock()
		return
	}
	c.sigAt = time.Now()
	c.sigMu.Unlock()
	if err := c.signal.Publish(ctx); err != nil {
		c.log.Debug("released-group signal not sent; peers take the groups on their next tick", "err", err.Error())
	}
}

// onReleasedSignal is a peer's reaction: reconcile soon, after a short random wait. A pod that is shutting down takes nothing, so it ignores it.
func (c *Coordinator) onReleasedSignal() {
	if c.isClosing() {
		return
	}
	time.AfterFunc(time.Duration(rand.Int63n(int64(signalJitter))), func() {
		c.signalled.Store(true) // the next pass off the trigger channel is counted as the signal's
		c.TriggerReconcile()
	})
}

// runSignal listens for peers' signals for the life of ctx.
func (c *Coordinator) runSignal(ctx context.Context) {
	if c.signal == nil {
		return
	}
	go c.signal.Subscribe(ctx, c.onReleasedSignal)
}
