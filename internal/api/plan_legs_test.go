package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/planleg"
	"github.com/apohor/rivolt/internal/rivian"
)

func TestWithLegCacheWrapsEveryPlannerButNotClientsWithout(t *testing.T) {
	mr := miniredis.RunT(t)
	d := Deps{PlanLegs: planleg.New(redis.NewClient(&redis.Options{Addr: mr.Addr()}), "t", nil)}
	uid := uuid.New()

	live := rivian.NewLive()
	if _, ok := withLegCache(d, uid, live).(legCachedClient); !ok {
		t.Error("the live client's planning should go through the cache and quota")
	}
	// The replay planners (stored plans, Valhalla-backed synthetic plans) are
	// limited like the live one.
	if _, ok := withLegCache(d, uid, fixtureClient()).(legCachedClient); !ok {
		t.Error("a replay-style planner should be wrapped too")
	}
	// A client with no planner has nothing to limit.
	for name, c := range map[string]rivian.Client{"stub": rivian.NewStub(), "mock": rivian.NewMock()} {
		if got := withLegCache(d, uid, c); got != c {
			t.Errorf("%s was wrapped", name)
		}
	}
	var nilLive *rivian.LiveClient
	if _, ok := withLegCache(d, uid, nilLive).(legCachedClient); ok {
		t.Error("a nil live client must not be wrapped")
	}
	// No Redis, no wrapping.
	if got := withLegCache(Deps{}, uid, live); got != rivian.Client(live) {
		t.Error("without a cache the client is returned as it is")
	}
	// A wrapped client is still a planner for the handlers.
	if _, ok := tripPlannerOf(withLegCache(d, uid, live)); !ok {
		t.Error("the wrapped live client must still be a TripPlanner")
	}
}

func TestSinglePlanQuotaIs429WithRetryAfter(t *testing.T) {
	c := plannerClient{Client: rivian.NewStub(), TripPlanner: errPlanner{&planleg.QuotaError{RetryAfter: 17*time.Minute + 30*time.Second}}}
	rec := httptest.NewRecorder()
	handleTripPlan(c, nil, nil, uuid.New(), nil, nil, nil, nil)(rec, httptest.NewRequest(http.MethodPost, "/api/trips/plan", bytes.NewBufferString(planBody)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "1050" {
		t.Errorf("Retry-After = %q, want 1050", got)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("plan_quota")) {
		t.Errorf("body = %s", rec.Body.String())
	}
	// Even a sliver of a second rounds up to a second, never zero.
	rec = httptest.NewRecorder()
	writeQuotaError(rec, &planleg.QuotaError{RetryAfter: 10 * time.Millisecond})
	if rec.Header().Get("Retry-After") != "1" {
		t.Errorf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
}
