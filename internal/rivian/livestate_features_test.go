package rivian

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

const featuresBody = `{"data":{"currentUser":{"vehicles":[
  {"id":"veh-a","vehicle":{"vehicleState":{"supportedFeatures":[{"name":"VEHICLE_CONNECTIVITY_PARALLAX","status":"AVAILABLE"}]}}},
  {"id":"veh-b","vehicle":{"vehicleState":{"supportedFeatures":[{"name":"VEHICLE_CONNECTIVITY_PARALLAX","status":"UNAVAILABLE"}]}}}]}}}`

// pod builds a monitor ("a pod") against a fake gateway that answers SupportedFeatures after delay and counts the calls.
func featuresPod(t *testing.T, hits *atomic.Int32, delay time.Duration, store LiveStateStore, fast bool) *StateMonitor {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		time.Sleep(delay)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, featuresBody)
	}))
	t.Cleanup(srv.Close)
	c := NewLive()
	c.userSessionToken = "u"
	c.endpoint = srv.URL + "/gateway"
	m := NewStateMonitor(c, slog.Default())
	m.fastTakeover = fast
	m.SetLiveStateStore(store)
	return m
}

// The first pod probes Rivian and leaves the map for every vehicle of the account; the next owner uses it at once, and Rivian is asked again in
// the background. With the flag off the call is on the path, as before.
func TestSupportedFeaturesAreSharedBetweenPods(t *testing.T) {
	store := newMemLiveStateStore()
	var hitsA, hitsB atomic.Int32
	a := featuresPod(t, &hitsA, 0, store, true)
	got, err := a.supportedFeaturesFor(context.Background(), "veh-a")
	if err != nil || got[FeatureConnectivityParallax] != FeatureStatusAvailable || hitsA.Load() != 1 {
		t.Fatalf("first pod: %v %v hits %d, want a probe", got, err, hitsA.Load())
	}
	if f, ok, _ := store.GetFeatures(context.Background(), "veh-b"); !ok || f[FeatureConnectivityParallax] != "UNAVAILABLE" {
		t.Errorf("the other vehicle of the account was not stored: %v %v", f, ok)
	}

	b := featuresPod(t, &hitsB, 400*time.Millisecond, store, true) // a slow gateway
	start := time.Now()
	got, err = b.supportedFeaturesFor(context.Background(), "veh-a")
	if err != nil || got[FeatureConnectivityParallax] != FeatureStatusAvailable {
		t.Fatalf("second pod: %v %v", got, err)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("the second pod waited %v for a map that was cached", d)
	}
	deadline := time.Now().Add(3 * time.Second)
	for hitsB.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if hitsB.Load() != 1 {
		t.Errorf("%d background refreshes, want 1", hitsB.Load())
	}

	var hitsC atomic.Int32
	c := featuresPod(t, &hitsC, 0, store, false) // flag off: always the call
	if _, err := c.supportedFeaturesFor(context.Background(), "veh-a"); err != nil || hitsC.Load() != 1 {
		t.Errorf("flag off: err %v hits %d, want the call", err, hitsC.Load())
	}
}

// A failed probe is an error for the caller, who stays on the vehicleState GPS, and nothing is stored.
func TestSupportedFeaturesProbeFailureStoresNothing(t *testing.T) {
	store := newMemLiveStateStore()
	var hits atomic.Int32
	m := featuresPod(t, &hits, 0, store, true)
	m.client.userSessionToken = "" // not authenticated: the call fails before the network
	if _, err := m.supportedFeaturesFor(context.Background(), "veh-a"); err == nil {
		t.Fatal("want an error")
	}
	if _, ok, _ := store.GetFeatures(context.Background(), "veh-a"); ok {
		t.Error("a failed probe left a map behind")
	}
}

// Through Redis (and the gated wrapper the app uses): a map round-trips under the user's live-state keys, so the account purge removes it with
// the rest, and it carries an expiry.
func TestFeaturesRoundTripThroughRedisAndTheGate(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := NewGatedLiveStateStore(NewRedisLiveStateStore(rdb, "uid-1", "rivolt"), nil)
	ctx := context.Background()
	if _, ok, err := store.GetFeatures(ctx, "veh-a"); ok || err != nil {
		t.Fatalf("empty: %v %v", ok, err)
	}
	want := map[string]string{FeatureConnectivityParallax: FeatureStatusAvailable}
	if err := store.PutFeatures(ctx, "veh-a", want, featuresTTL); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetFeatures(ctx, "veh-a")
	if err != nil || !ok || got[FeatureConnectivityParallax] != FeatureStatusAvailable {
		t.Fatalf("round trip: %v %v %v", got, ok, err)
	}
	if ttl := mr.TTL("rivolt:livestate:uid-1:veh-a:features"); ttl <= 0 {
		t.Errorf("no expiry on the features key (%v)", ttl)
	}
	if n, err := (&LiveStatePurger{Rdb: rdb, Prefix: "rivolt"}).PurgeNow(ctx, "uid-1"); err != nil || n != 1 {
		t.Errorf("the account purge removed %d keys (%v), want the features key", n, err)
	}
}
