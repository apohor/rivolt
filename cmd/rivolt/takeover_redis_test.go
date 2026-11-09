package main

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestTakeoverRedisHonoursTheContextAndKeepsTheCredentials(t *testing.T) {
	if takeoverRedis(nil) != nil {
		t.Fatal("no shared Redis must mean no limiter Redis")
	}
	shared := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", Username: "u", Password: "p", PoolSize: 20, DB: 0})
	defer func() { _ = shared.Close() }()
	c := takeoverRedis(shared)
	defer func() { _ = c.Close() }()
	o := c.Options()
	if !o.ContextTimeoutEnabled || o.PoolSize != takeoverRedisPoolSize || o.Addr != "127.0.0.1:1" || o.Username != "u" || o.Password != "p" {
		t.Errorf("options %+v", o)
	}
	if shared.Options().ContextTimeoutEnabled || shared.Options().PoolSize != 20 {
		t.Error("the shared client's options were changed")
	}
}
