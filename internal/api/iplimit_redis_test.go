package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

var testRateKey = RateLimiterKey(testFailKey)

type ratePods struct {
	mr       *miniredis.Miniredis
	pods     []*ipLimiter
	backends []*sharedBuckets
	fallback atomic.Int32
}

// newRatePods builds n replicas of one limiter (perMin a minute) over one Redis.
func newRatePods(t *testing.T, n, perMin int) *ratePods {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	mr.SetTime(time.Unix(1_700_000_000, 0))
	rp := &ratePods{mr: mr}
	for i := 0; i < n; i++ {
		rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		sb := &sharedBuckets{rdb: rdb, prefix: "rivolt", key: testRateKey, now: time.Now, onFallback: func(string) { rp.fallback.Add(1) }}
		l := newIPLimiter(perMin, time.Minute, time.Hour)
		l.name = "test-route"
		l.shared = sb
		rp.pods = append(rp.pods, l)
		rp.backends = append(rp.backends, sb)
	}
	return rp
}

func TestRateLimitIsCountedAcrossReplicas(t *testing.T) {
	rp := newRatePods(t, 3, 10)
	// Ten requests from one address, spread over three pods: none has more than 4 itself.
	for i := 0; i < 10; i++ {
		if ok, _ := rp.pods[i%3].allowN("192.0.2.1", 1); !ok {
			t.Fatalf("request %d refused before the limit", i+1)
		}
	}
	for i, p := range rp.pods {
		ok, wait := p.allowN("192.0.2.1", 1)
		if ok {
			t.Fatalf("replica %d allowed the 11th request across replicas", i)
		}
		if wait <= 0 || wait > time.Minute {
			t.Fatalf("replica %d: wait %v, want 0 < wait <= 1m", i, wait)
		}
	}
	// Another address has its own bucket.
	if ok, _ := rp.pods[1].allowN("192.0.2.2", 1); !ok {
		t.Fatal("a different address was refused")
	}
	if rp.fallback.Load() != 0 {
		t.Fatal("a fallback was counted with Redis up")
	}
}

func TestRateLimitRefillsByRedisClock(t *testing.T) {
	rp := newRatePods(t, 2, 6) // one token every ten seconds
	for i := 0; i < 6; i++ {
		rp.pods[0].allowN("192.0.2.1", 1)
	}
	if ok, _ := rp.pods[1].allowN("192.0.2.1", 1); ok {
		t.Fatal("allowed with the bucket empty")
	}
	rp.mr.SetTime(time.Unix(1_700_000_000, 0).Add(11 * time.Second))
	// Redis time moved; the pod's own bucket is on the real clock, so check the shared verdict directly.
	ok, _, answered := rp.backends[1].allow("test-route", "192.0.2.1", 6.0/60, 6, 1)
	if !answered || !ok {
		t.Fatalf("ok=%v answered=%v, want one token back after 11 s", ok, answered)
	}
	ok, _, _ = rp.backends[0].allow("test-route", "192.0.2.1", 6.0/60, 6, 1)
	if ok {
		t.Fatal("a second token came back after 11 s")
	}
}

func TestRateLimitCostIsSharedToo(t *testing.T) {
	// A byte budget: 1000 a minute, taken 400 at a time on different pods.
	rp := newRatePods(t, 3, 1000)
	if ok, _ := rp.pods[0].allowN("192.0.2.1", 400); !ok {
		t.Fatal("first 400 refused")
	}
	if ok, _ := rp.pods[1].allowN("192.0.2.1", 400); !ok {
		t.Fatal("second 400 refused")
	}
	if ok, _ := rp.pods[2].allowN("192.0.2.1", 400); ok {
		t.Fatal("third 400 allowed: 1200 > 1000 across replicas")
	}
	// More than the whole budget is never satisfiable.
	if ok, wait := rp.pods[0].allowN("192.0.2.9", 5000); ok || wait <= 0 {
		t.Fatalf("5000 against a 1000 budget: ok=%v wait=%v", ok, wait)
	}
}

func TestRateLimitFallsBackPerPodWhenRedisIsDown(t *testing.T) {
	rp := newRatePods(t, 2, 10)
	rp.mr.Close()
	// Never open: each pod still enforces the limit itself.
	allowed := 0
	for i := 0; i < 25; i++ {
		if ok, _ := rp.pods[0].allowN("192.0.2.1", 1); ok {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("allowed %d with Redis down, want the per-pod limit of 10", allowed)
	}
	if rp.fallback.Load() == 0 {
		t.Fatal("no fallback counted")
	}
	// A pod that used none of its own bucket gets its own ten, as before Redis was used.
	if ok, _ := rp.pods[1].allowN("192.0.2.1", 1); !ok {
		t.Fatal("the other pod refused its first request")
	}
}

func TestRateLimitNeverTreatsAnErrorAsRefusal(t *testing.T) {
	rp := newRatePods(t, 1, 10)
	rp.mr.Close()
	if ok, _ := rp.pods[0].allowN("192.0.2.1", 1); !ok {
		t.Fatal("a Redis error refused a request the per-pod bucket would allow")
	}
}

func TestRateLimitRecoversWhenRedisReturns(t *testing.T) {
	rp := newRatePods(t, 2, 10)
	addr := rp.mr.Addr()
	rp.mr.Close()
	rp.pods[0].allowN("192.0.2.1", 1)
	if rp.backends[0].downUntil.Load() == 0 {
		t.Fatal("no backoff after a failure")
	}
	mr2 := miniredis.NewMiniRedis()
	if err := mr2.StartAddr(addr); err != nil {
		t.Skipf("cannot rebind %s: %v", addr, err)
	}
	t.Cleanup(mr2.Close)
	mr2.SetTime(time.Unix(1_700_000_000, 0))
	rp.backends[0].downUntil.Store(0)
	rp.backends[1].downUntil.Store(0)
	before := rp.fallback.Load()
	for i := 0; i < 9; i++ {
		rp.pods[1].allowN("192.0.2.7", 1)
	}
	if ok, _ := rp.pods[0].allowN("192.0.2.7", 1); !ok {
		t.Fatal("10th across replicas refused")
	}
	if ok, _ := rp.pods[0].allowN("192.0.2.7", 1); ok {
		t.Fatal("11th across replicas allowed after Redis returned")
	}
	if rp.fallback.Load() != before {
		t.Fatal("fallbacks counted after Redis returned")
	}
}

func TestRateLimitKeysCarryNoAddress(t *testing.T) {
	rp := newRatePods(t, 1, 10)
	rp.pods[0].allowN("192.0.2.55", 1)
	keys := rp.mr.Keys()
	if len(keys) != 1 {
		t.Fatalf("keys = %v, want one", keys)
	}
	if strings.Contains(keys[0], "192.0.2") || !strings.HasPrefix(keys[0], "rivolt:rl:test-route:") {
		t.Fatalf("key %q names the address or is outside the prefix", keys[0])
	}
	if ttl := rp.mr.TTL(keys[0]); ttl <= 0 {
		t.Fatalf("key has no expiry (ttl %v)", ttl)
	}
}

func TestUnnamedOrUnkeyedLimiterStaysPerPod(t *testing.T) {
	rp := newRatePods(t, 2, 5)
	rp.pods[0].name = ""
	rp.pods[1].name = ""
	for i := 0; i < 5; i++ {
		rp.pods[0].allowN("192.0.2.1", 1)
	}
	if ok, _ := rp.pods[1].allowN("192.0.2.1", 1); !ok {
		t.Fatal("unnamed limiters shared a bucket")
	}
	if len(rp.mr.Keys()) != 0 {
		t.Fatal("an unnamed limiter wrote to Redis")
	}
	var nilSet *limiterSet
	l := nilSet.ip("x", 5, time.Minute, time.Hour)
	if l.shared != nil {
		t.Fatal("a nil limiterSet built a shared limiter")
	}
}

// podSet is a limiterSet for one replica over the shared Redis of rp.
func (rp *ratePods) podSet(i int) *limiterSet { return &limiterSet{shared: rp.backends[i]} }

// requestsFrom sends n GETs from addr through h and returns how many were answered 429.
func requestsFrom(h http.Handler, addr string, n int) (refused int) {
	for i := 0; i < n; i++ {
		req := httptest.NewRequest(http.MethodGet, "/route", nil)
		req.RemoteAddr = addr + ":4000"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			refused++
		}
	}
	return refused
}

func TestMapsAndDemoLimitsHoldAcrossReplicas(t *testing.T) {
	rp := newRatePods(t, 2, 10)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	// Valhalla is 60 a minute per caller. 40 on each pod is under the limit per pod and over it together.
	a := rp.podSet(0).user("maps-valhalla", mapsValhallaPerMin).Middleware(ok)
	b := rp.podSet(1).user("maps-valhalla", mapsValhallaPerMin).Middleware(ok)
	refused := requestsFrom(a, "192.0.2.1", 40) + requestsFrom(b, "192.0.2.1", 40)
	if refused != 20 {
		t.Fatalf("%d of 80 refused across two replicas, want 20 (limit 60)", refused)
	}
	// The demo's install-wide limiter is one bucket for every replica.
	g0 := rp.podSet(0).ip("demo-access-global", 30, time.Minute, time.Hour)
	g0.key = func(*http.Request) string { return "global" }
	g1 := rp.podSet(1).ip("demo-access-global", 30, time.Minute, time.Hour)
	g1.key = func(*http.Request) string { return "global" }
	refused = requestsFrom(g0.Middleware(ok), "192.0.2.10", 20) + requestsFrom(g1.Middleware(ok), "192.0.2.11", 20)
	if refused != 10 {
		t.Fatalf("%d of 40 refused by the global demo bucket, want 10 (limit 30)", refused)
	}
}

// Every named limiter is one Redis bucket: two routes that reuse a name would take from each other's budget.
func TestLimiterNamesAreUnique(t *testing.T) {
	re := regexp.MustCompile(`\bls\.(?:ip|user)\("([a-z0-9-]+)"|newDemo(?:Global)?Limiter\(ls, "([a-z0-9-]+)"\)`)
	seen := map[string]string{}
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			name := m[1] + m[2]
			if prev, dup := seen[name]; dup {
				t.Errorf("limiter name %q is used in %s and %s", name, prev, f)
			}
			seen[name] = f
		}
	}
	if len(seen) < 30 {
		t.Fatalf("found %d named limiters, want at least 30 (the scan is stale)", len(seen))
	}
}
