package rivian

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// The per-user data-plane reads must not hold the client lock across the upstream
// call (review §4.2): with one request blocked at the gateway, Authenticated()
// (which the monitor and API handlers call) still answers.
func TestDataPlaneReadsDoNotHoldTheClientLock(t *testing.T) {
	calls := map[string]func(c *LiveClient) error{
		"Vehicles":                  func(c *LiveClient) error { _, err := c.Vehicles(context.Background()); return err },
		"State":                     func(c *LiveClient) error { _, err := c.State(context.Background(), "veh-1"); return err },
		"ChargingSchedules":         func(c *LiveClient) error { _, err := c.ChargingSchedules(context.Background(), "veh-1"); return err },
		"OTAUpdateDetails":          func(c *LiveClient) error { _, err := c.OTAUpdateDetails(context.Background(), "veh-1"); return err },
		"VehicleImages":             func(c *LiveClient) error { _, err := c.VehicleImages(context.Background()); return err },
		"CompletedSessionSummaries": func(c *LiveClient) error { _, err := c.CompletedSessionSummaries(context.Background()); return err },
		"SessionCharger":            func(c *LiveClient) error { _, err := c.SessionCharger(context.Background(), "txn-1"); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			inFlight := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				once.Do(func() { close(inFlight) })
				<-release
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"data":{}}`)
			}))
			defer server.Close()
			released := false
			defer func() {
				if !released {
					close(release)
				}
			}()

			c := NewLive()
			c.endpoint = server.URL + "/gateway"
			c.chargingEndpoint = server.URL + "/charging"
			c.userSessionToken, c.appSessionToken, c.csrfToken = "u", "a", "c"

			done := make(chan struct{})
			go func() { _ = call(c); close(done) }()
			select {
			case <-inFlight:
			case <-time.After(5 * time.Second):
				t.Fatal("the request never reached the gateway")
			}
			answered := make(chan bool, 1)
			go func() { answered <- c.Authenticated() }()
			select {
			case ok := <-answered:
				if !ok {
					t.Error("Authenticated() = false for a signed-in client")
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s holds the client lock across the upstream call", name)
			}
			close(release)
			released = true
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the call never returned")
			}
		})
	}
}
