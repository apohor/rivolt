package rivian

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A slow trip-plan call (up to 30 s upstream) must not hold the per-user client
// lock: Authenticated(), which the monitor and API handlers call, answered only
// after the plan returned (review §4.2).
func TestPlanTripDoesNotHoldTheClientLockAcrossTheUpstreamCall(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(string(b), "planTripWithMultiStopV2") {
			close(inFlight)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"planTrip2":{"status":"OK","plans":[]}}}`)
	}))
	defer server.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.userSessionToken, c.appSessionToken, c.csrfToken = "u", "a", "c"

	planned := make(chan error, 1)
	go func() {
		_, err := c.PlanTrip(context.Background(), PlanTripInput{VehicleID: "veh-1", StartingSoC: 80, Waypoints: []PlanTripWaypoint{
			{Latitude: 34, Longitude: -118, WaypointType: "origin"},
			{Latitude: 35, Longitude: -118, WaypointType: "destination"},
		}})
		planned <- err
	}()
	select {
	case <-inFlight:
	case <-time.After(5 * time.Second):
		t.Fatal("the plan request never reached the upstream")
	}

	answered := make(chan bool, 1)
	go func() { answered <- c.Authenticated() }()
	select {
	case ok := <-answered:
		if !ok {
			t.Error("Authenticated() = false for a signed-in client")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Authenticated() blocked behind the in-flight trip plan: the client lock is held across the upstream call")
	}

	close(release)
	select {
	case err := <-planned:
		if err != nil {
			t.Errorf("PlanTrip: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PlanTrip never returned")
	}
}

// The csrf heal still works without the lock held: a stale-token rejection
// refreshes a-sess/csrf (under the lock, briefly) and the retry succeeds.
func TestPlanTripHealsStaleCSRFWithoutHoldingTheLock(t *testing.T) {
	plans, csrfs := 0, 0
	var lastCSRF string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(string(b), "CreateCSRFToken"):
			csrfs++
			_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"fresh-c","appSessionToken":"fresh-a"}}}`)
		default:
			plans++
			lastCSRF = r.Header.Get("csrf-token")
			if plans == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
				return
			}
			_, _ = io.WriteString(w, `{"data":{"planTrip2":{"status":"OK","plans":[]}}}`)
		}
	}))
	defer server.Close()

	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.userSessionToken, c.appSessionToken, c.csrfToken = "u", "stale-a", "stale-c"
	_, err := c.PlanTrip(context.Background(), PlanTripInput{VehicleID: "veh-1", StartingSoC: 80, Waypoints: []PlanTripWaypoint{
		{Latitude: 34, Longitude: -118, WaypointType: "origin"},
		{Latitude: 35, Longitude: -118, WaypointType: "destination"},
	}})
	if err != nil {
		t.Fatalf("PlanTrip after a heal: %v", err)
	}
	if plans != 2 || csrfs != 1 || lastCSRF != "fresh-c" {
		t.Errorf("plans=%d csrf refreshes=%d retry csrf=%q, want 2, 1, fresh-c", plans, csrfs, lastCSRF)
	}
	if needs, _ := c.NeedsReauth(); needs {
		t.Error("a healed call flipped needs_reauth")
	}
}

// Another call heals (mints a new a-sess/csrf pair) while a detached plan is in
// flight. The plan then fails on the pair it was sent with; it must retry with
// the current pair, not mint a second one that would invalidate the first
// (a CreateCSRFToken ping-pong against Rivian).
func TestDetachedHealReusesAPairMintedMeanwhile(t *testing.T) {
	inFlight := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	plans, csrfs := 0, 0
	var retryCSRF string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(b), "CreateCSRFToken") {
			mu.Lock()
			csrfs++
			n := csrfs
			mu.Unlock()
			_, _ = io.WriteString(w, fmt.Sprintf(`{"data":{"createCsrfToken":{"csrfToken":"csrf-%d","appSessionToken":"asess-%d"}}}`, n, n))
			return
		}
		mu.Lock()
		plans++
		n := plans
		mu.Unlock()
		if n == 1 {
			close(inFlight)
			<-release
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
			return
		}
		mu.Lock()
		retryCSRF = r.Header.Get("csrf-token")
		mu.Unlock()
		_, _ = io.WriteString(w, `{"data":{"planTrip2":{"status":"OK","plans":[]}}}`)
	}))
	defer server.Close()

	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.userSessionToken, c.appSessionToken, c.csrfToken = "u", "stale-a", "stale-c"

	done := make(chan error, 1)
	go func() {
		_, err := c.PlanTrip(context.Background(), PlanTripInput{VehicleID: "veh-1", StartingSoC: 80, Waypoints: []PlanTripWaypoint{
			{Latitude: 34, Longitude: -118, WaypointType: "origin"},
			{Latitude: 35, Longitude: -118, WaypointType: "destination"},
		}})
		done <- err
	}()
	<-inFlight
	// Meanwhile a locked call heals first.
	c.mu.Lock()
	err := c.forceRefreshCSRFLocked(context.Background())
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PlanTrip: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("PlanTrip never returned")
	}
	mu.Lock()
	defer mu.Unlock()
	if csrfs != 1 {
		t.Errorf("%d CreateCSRFToken calls, want exactly 1 (the other call's)", csrfs)
	}
	if retryCSRF != "csrf-1" {
		t.Errorf("the retry carried %q, want the pair minted meanwhile (csrf-1)", retryCSRF)
	}
}
