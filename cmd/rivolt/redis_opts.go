package main

import (
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Pool sizes (single-owner C5): every Redis client sets PoolSize explicitly, because go-redis's default is 10 x GOMAXPROCS, so the
// connection count would depend on the pod's CPU and not on a number anyone chose. Valkey's maxclients is 10000; at N=20 pods the
// connections are 20 x (shared + forwarder + the short-lived preflight/CLI clients). The shared client carries the rate limiter's
// script calls, the live-state publish/read and the plan-leg cache, all short commands: 20 is well above their concurrency (the
// limiter is bounded by the Rivian call rate, the live state by the vehicles this pod streams). The forwarder has its own client so a
// stuck BLPOP cannot starve them: 1 owner BLPOP + 1 readiness SET + up to 8 requesters (the worker cap) = 10.
const (
	sharedRedisPoolSize     = 20
	forwardRedisPoolSize    = 10
	oneShotRedisPoolSize    = 2 // the preflight and other one-shot CLIs
	envRedisPoolSize        = "RIVOLT_REDIS_POOL_SIZE"
	maxRedisPoolSizeAllowed = 100
)

// redisOptions is the client configuration for RIVOLT_REDIS_ADDR. RIVOLT_REDIS_USERNAME and
// RIVOLT_REDIS_PASSWORD, both optional, sign in as a Valkey ACL user (one per environment, restricted
// to its key prefix); both unset is the old behaviour, no AUTH. They come from the app's Secret, so
// surrounding whitespace (a trailing newline from how a value was stored) is dropped.
func redisOptions(addr string, getenv func(string) string) *redis.Options {
	return &redis.Options{
		Addr:     addr,
		Username: strings.TrimSpace(getenv("RIVOLT_REDIS_USERNAME")),
		Password: strings.TrimSpace(getenv("RIVOLT_REDIS_PASSWORD")),
		PoolSize: redisPoolSize(getenv, sharedRedisPoolSize),
	}
}

// redisPoolSize returns RIVOLT_REDIS_POOL_SIZE (1 to maxRedisPoolSizeAllowed) when set, else def. A bad value falls back to def.
func redisPoolSize(getenv func(string) string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(getenv(envRedisPoolSize))); err == nil && n >= 1 && n <= maxRedisPoolSizeAllowed {
		return n
	}
	return def
}

// forwardRedisOptions is the forwarder's own client: pool forwardRedisPoolSize, and a context deadline cuts a blocking BLPOP short
// (go-redis otherwise ignores the context while it waits on the socket).
func forwardRedisOptions(addr string, getenv func(string) string) *redis.Options {
	o := redisOptions(addr, getenv)
	o.PoolSize = forwardRedisPoolSize
	o.ContextTimeoutEnabled = true
	return o
}

// oneShotRedisOptions is for clients that make a few calls and exit.
func oneShotRedisOptions(addr string, getenv func(string) string) *redis.Options {
	o := redisOptions(addr, getenv)
	o.PoolSize = oneShotRedisPoolSize
	return o
}

// redisAuthAttrs describes the client's sign-in for the boot log: off, or on with the user name.
// Never the password.
func redisAuthAttrs(o *redis.Options) []any {
	if o.Password == "" && o.Username == "" {
		return []any{"auth", "off"}
	}
	user := o.Username
	if user == "" {
		user = "default"
	}
	return []any{"auth", "on", "user", user}
}
