//go:build integration

package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/api"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/testpg"
)

// RIVOLT_ROLE api next to an all pod, over the real lease store and Postgres, with the pieces main.go wires for a leaseless role: the api
// store follows the vehicle -> group map and writes no lease row while the all pod leads; it knows the vehicle's group (so the forwarder can
// route) but its fence reads Lost for that group, for the vehicle and for the user; and SIGUSR1 fails its /readyz with no coordinator.
func TestAPIRoleTakesNoLeases(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	uid, err := db.EnsureUser(ctx, pool, "role-api")
	if err != nil {
		t.Fatal(err)
	}
	const veh = "veh-role-api"
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, $2)`, uid, veh); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO user_secrets (user_id, name, ciphertext, kek_id) VALUES ($1, 'rivian.session', '\x00', 'k')`, uid); err != nil {
		t.Fatal(err)
	}
	store := func(pod string) *leases.Store {
		s, err := leases.NewStore(pool, pod)
		if err != nil {
			t.Fatal(err)
		}
		s.SetGroupLead(true)
		return s
	}
	allPod, apiPod := store("pod-all"), store("pod-api")

	if _, err := allPod.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	vg, err := allPod.GroupVehicles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allPod.SetVehicleGroups(vg)
	if _, st := allPod.Fence(veh); st != fence.Held {
		t.Fatalf("the all pod: %v, want Held", st)
	}

	fctx, stop := context.WithCancel(ctx)
	defer stop()
	go apiPod.FollowVehicleGroups(fctx, func(err error) { t.Errorf("follow: %v", err) })
	var group string
	for dl := time.Now().Add(10 * time.Second); time.Now().Before(dl); time.Sleep(20 * time.Millisecond) {
		if g, ok := apiPod.GroupOfVehicle(veh); ok {
			group = g
			break
		}
	}
	if group == "" || group != vg[veh] {
		t.Fatalf("api pod's group for the vehicle %q, want %q", group, vg[veh])
	}
	// A second lead pass on the all pod while the api pod follows: still one row, the all pod's.
	if _, err := allPod.LeadGroups(ctx); err != nil {
		t.Fatal(err)
	}
	var apiRows, allRows int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE pod_id = 'pod-api'), count(*) FILTER (WHERE pod_id = 'pod-all') FROM account_leases`).Scan(&apiRows, &allRows); err != nil {
		t.Fatal(err)
	}
	if apiRows != 0 || allRows != 1 {
		t.Errorf("lease rows: api %d, all %d; want 0 and 1", apiRows, allRows)
	}
	if _, st := apiPod.Fence(veh); st != fence.Lost {
		t.Errorf("api pod's fence for a vehicle the all pod holds: %v, want Lost", st)
	}
	if _, st, err := apiPod.FenceUser(ctx, uid.String()); err != nil || st != fence.Lost {
		t.Errorf("api pod's user fence: %v %v, want Lost", st, err)
	}

	h := api.New(api.Deps{SystemDB: pool})
	readyz := func() int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://rivolt.test/readyz", nil))
		return w.Code
	}
	if c := readyz(); c != http.StatusOK {
		t.Fatalf("/readyz before the drain: %d", c)
	}
	done := make(chan struct{}, 1)
	drain := drainFunc(api.StartDrain, nil, func() {}, slog.New(slog.DiscardHandler))
	go watchDrain(notifyDrain(), func() { drain(); done <- struct{}{} })
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("SIGUSR1 did not reach the drain")
	}
	if c := readyz(); c != http.StatusServiceUnavailable {
		t.Errorf("/readyz after SIGUSR1: %d, want 503", c)
	}
}
