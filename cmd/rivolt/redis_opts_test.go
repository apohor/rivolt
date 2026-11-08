package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const testRedisPassword = "pw-7c-3f9a1c0e5b8d4e2a9c6f1b7d0e4a8c2f" // never expected in any output

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Unset credentials are the old behaviour: no AUTH.
func TestRedisOptionsUnsetIsNoAuth(t *testing.T) {
	o := redisOptions("r:6379", envOf(nil))
	if o.Addr != "r:6379" || o.Username != "" || o.Password != "" {
		t.Fatalf("options = %+v, want the address only", o)
	}
	if got := redisAuthAttrs(o); len(got) != 2 || got[1] != "off" {
		t.Errorf("auth attrs = %v, want auth off", got)
	}
}

// The right ACL user signs in; whitespace around the stored values is dropped.
func TestRedisOptionsSignsInAsACLUser(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.RequireUserAuth("rivolt", testRedisPassword)
	o := redisOptions(mr.Addr(), envOf(map[string]string{
		"RIVOLT_REDIS_USERNAME": " rivolt\n",
		"RIVOLT_REDIS_PASSWORD": testRedisPassword + "\n",
	}))
	rdb := redis.NewClient(o)
	defer func() { _ = rdb.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Set(ctx, "rivolt:rl:x", "1", time.Minute).Err(); err != nil {
		t.Fatalf("SET as the ACL user: %v", err)
	}
	if got := redisAuthAttrs(o); len(got) != 4 || got[1] != "on" || got[3] != "rivolt" {
		t.Errorf("auth attrs = %v, want auth on, user rivolt", got)
	}
}

// The password never reaches a log line or an error string: not from the boot log, not from a
// rejected sign-in, not from a connection that fails. go-redis errors can carry connection
// details, so each path is checked.
func TestRedisPasswordNeverInLogsOrErrors(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	env := envOf(map[string]string{"RIVOLT_REDIS_USERNAME": "rivolt", "RIVOLT_REDIS_PASSWORD": testRedisPassword})

	// The boot log line, as main writes it.
	o := redisOptions("127.0.0.1:1", env)
	logger.Info("redis: client", append([]any{"addr", o.Addr}, redisAuthAttrs(o)...)...)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var errs []string

	// Rejected: the server knows the user with another password.
	mr := miniredis.RunT(t)
	mr.RequireUserAuth("rivolt", "a-different-password")
	bad := redis.NewClient(redisOptions(mr.Addr(), env))
	defer func() { _ = bad.Close() }()
	if err := bad.Ping(ctx).Err(); err == nil {
		t.Fatal("ping with the wrong password succeeded")
	} else {
		errs = append(errs, err.Error())
		logger.Warn("ratelimit: redis unreachable", "err", err.Error())
	}

	// Unreachable: nothing listens there.
	down := redis.NewClient(redisOptions("127.0.0.1:1", env))
	defer func() { _ = down.Close() }()
	if err := down.Ping(ctx).Err(); err == nil {
		t.Fatal("ping to a closed port succeeded")
	} else {
		errs = append(errs, err.Error())
		logger.Warn("ratelimit: redis unreachable", "err", err.Error())
	}

	for _, e := range errs {
		if strings.Contains(e, testRedisPassword) {
			t.Errorf("error string carries the password: %q", e)
		}
	}
	if strings.Contains(buf.String(), testRedisPassword) {
		t.Errorf("log output carries the password:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "auth=on") || !strings.Contains(buf.String(), "user=rivolt") {
		t.Errorf("boot log does not say auth on and the user:\n%s", buf.String())
	}
}

// Single-owner C5: every Redis client sets its pool size, so the connection count is a number someone chose and not 10 x GOMAXPROCS.
func TestRedisPoolSizesAreExplicit(t *testing.T) {
	if o := redisOptions("r:6379", envOf(nil)); o.PoolSize != 20 {
		t.Errorf("shared client pool = %d, want 20", o.PoolSize)
	}
	if o := redisOptions("r:6379", envOf(map[string]string{"RIVOLT_REDIS_POOL_SIZE": " 30\n"})); o.PoolSize != 30 {
		t.Errorf("RIVOLT_REDIS_POOL_SIZE=30 gave %d", o.PoolSize)
	}
	for _, bad := range []string{"0", "-3", "101", "many", ""} {
		if o := redisOptions("r:6379", envOf(map[string]string{"RIVOLT_REDIS_POOL_SIZE": bad})); o.PoolSize != 20 {
			t.Errorf("RIVOLT_REDIS_POOL_SIZE=%q gave %d, want the default 20", bad, o.PoolSize)
		}
	}
	f := forwardRedisOptions("r:6379", envOf(map[string]string{"RIVOLT_REDIS_USERNAME": "rivolt", "RIVOLT_REDIS_POOL_SIZE": "50"}))
	if f.PoolSize != 10 || !f.ContextTimeoutEnabled || f.Username != "rivolt" {
		t.Errorf("forwarder client = pool %d, context timeout %v, user %q; want 10, true, rivolt (its own size, not the shared override)", f.PoolSize, f.ContextTimeoutEnabled, f.Username)
	}
	if o := oneShotRedisOptions("r:6379", envOf(nil)); o.PoolSize != 2 {
		t.Errorf("one-shot client pool = %d, want 2", o.PoolSize)
	}
}
