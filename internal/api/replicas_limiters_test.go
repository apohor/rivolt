package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/metrics"
)

// Scenario 9 of the N-instance harness (replicas postmortem, infra #224): the rate limiters count across pods while Redis answers
// and fall back per pod when it does not. Three replicas are built the way main builds each one, Deps.limiterSet() over the
// install's Redis with its own metrics and logger, and driven through the real middleware. The assertions sum over all replicas, so
// they hold for any N; CI runs three.
func TestReplicasLimitersCountAcrossPodsAndFallBackPerPod(t *testing.T) {
	const (
		pods  = 3
		limit = 12
		each  = 30 // requests each pod gets from one address
	)
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	redisAddr := mr.Addr()
	mr.SetTime(time.Unix(1_700_000_000, 0))

	type pod struct {
		m    *metrics.Metrics
		log  *bytes.Buffer
		set  *limiterSet
		byIP http.Handler
		user http.Handler
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	var ps []*pod
	for i := 0; i < pods; i++ {
		rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
		t.Cleanup(func() { _ = rdb.Close() })
		p := &pod{m: metrics.New(), log: &bytes.Buffer{}}
		p.set = Deps{Redis: rdb, RedisPrefix: "rivolt", FailLimiterKey: testFailKey, Metrics: p.m, Logger: slog.New(slog.NewTextHandler(p.log, nil))}.limiterSet()
		if p.set == nil {
			t.Fatal("no limiter set with Redis and a key")
		}
		p.byIP = p.set.ip("scn9-ip", limit, time.Minute, time.Hour).Middleware(ok)
		p.user = p.set.user("scn9-user", limit).Middleware(ok)
		ps = append(ps, p)
	}
	refusedAcross := func(addr string, pick func(*pod) http.Handler) (refused int) {
		for _, p := range ps {
			refused += requestsFrom(pick(p), addr, each)
		}
		return refused
	}
	fallbacks := func(name string) (n int) {
		for _, p := range ps {
			n += int(testutil.ToFloat64(p.m.RateLimiterFallbackTotal.WithLabelValues(name)))
		}
		return n
	}
	byIP := func(p *pod) http.Handler { return p.byIP }
	byUser := func(p *pod) http.Handler { return p.user }

	// Redis up: the limit is the limit across every pod, not limit x pods, for both kinds of limiter.
	for name, pick := range map[string]func(*pod) http.Handler{"ip": byIP, "user": byUser} {
		if got, want := refusedAcross("192.0.2.1", pick), pods*each-limit; got != want {
			t.Errorf("%s: %d of %d refused across %d pods, want %d (limit %d)", name, got, pods*each, pods, want, limit)
		}
	}
	if n := fallbacks("scn9-ip") + fallbacks("scn9-user"); n != 0 {
		t.Errorf("%d fallbacks counted with Redis up", n)
	}

	// Redis gone: never open, never closed by the error. Each pod enforces the limit on its own, every call it serves that way is
	// counted, and each pod says so once per backoff window.
	mr.Close()
	for name, pick := range map[string]func(*pod) http.Handler{"ip": byIP, "user": byUser} {
		if got, want := refusedAcross("192.0.2.2", pick), pods*(each-limit); got != want {
			t.Errorf("%s with Redis down: %d refused, want %d (%d pods each holding their own limit of %d)", name, got, want, pods, limit)
		}
	}
	for _, name := range []string{"scn9-ip", "scn9-user"} {
		if got, want := fallbacks(name), pods*each; got != want {
			t.Errorf("%s: %d fallbacks counted, want %d (every call served per pod)", name, got, want)
		}
	}
	for i, p := range ps {
		out := p.log.String()
		// One line per pod per backoff window (the backoff is the pod's, shared by its limiters), naming the limiter that met it.
		if n := strings.Count(out, "rate limiter fell back"); n != 1 || !strings.Contains(out, "limiter=scn9-") {
			t.Errorf("pod %d logged %d fallback lines in one backoff window, want 1 naming a limiter:\n%s", i, n, out)
		}
		if strings.Contains(out, "192.0.2") {
			t.Errorf("pod %d logged an address:\n%s", i, out)
		}
	}

	// Redis back: the pods count together again once their backoff ends.
	// A fake Redis that cannot come back on the same address fails the test: a skip here would drop the "Redis back" check in CI
	// without anyone seeing it. The port is retried for a moment first, since the kernel may still hold it.
	back := miniredis.NewMiniRedis()
	var bindErr error
	for i := 0; i < 50; i++ {
		if bindErr = back.StartAddr(redisAddr); bindErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if bindErr != nil {
		t.Fatalf("cannot restart the fake Redis on %s: %v", redisAddr, bindErr)
	}
	t.Cleanup(back.Close)
	back.SetTime(time.Unix(1_700_000_000, 0))
	for _, p := range ps {
		p.set.shared.downUntil.Store(0)
	}
	before := fallbacks("scn9-ip")
	if got, want := refusedAcross("192.0.2.3", byIP), pods*each-limit; got != want {
		t.Errorf("after Redis returned: %d refused across pods, want %d", got, want)
	}
	if fallbacks("scn9-ip") != before {
		t.Error("fallbacks counted after Redis returned")
	}
}
