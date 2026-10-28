package main

import (
	"strings"

	"github.com/redis/go-redis/v9"
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
	}
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
