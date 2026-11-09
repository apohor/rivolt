package main

import "github.com/redis/go-redis/v9"

// takeoverRedis is the takeover limiter's own Redis client: the shared client's options, a small pool, and ContextTimeoutEnabled so the limiter's
// 300 ms bound on one Redis call holds. go-redis otherwise ignores the context while it waits on the socket, so on a Redis that accepts the
// connection and never answers, the shared client costs every takeover call its 3 s ReadTimeout (and 5 s to dial) before the limiter fails open.
// nil in, nil out (no Redis: the limiter is local only).
func takeoverRedis(shared *redis.Client) *redis.Client {
	if shared == nil {
		return nil
	}
	o := *shared.Options()
	o.ContextTimeoutEnabled = true
	o.PoolSize = 4
	o.MinIdleConns = 0
	return redis.NewClient(&o)
}
