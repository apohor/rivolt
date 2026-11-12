//go:build integration

package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/leases"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// A3 (zero-downtime design §3.1c, step 3): on an api-role pod with permit enforce on, GET /api/vehicles reaches no Rivian endpoint. The pod
// holds no lease, so its Rivian calls carry no permit; the list has to come from Postgres (the owner keeps the rows current) and the pod is never
// refused. The Rivian client here is the real LiveClient pointed at a counting fake gateway, so "0 calls" is counted where Rivian would count it
// and "0 refused" is the permit's own counter. The negative control (owner discovery off) shows the same setup does catch a pod that asks Rivian.

type opCounter struct {
	srv *httptest.Server
	mu  sync.Mutex
	n   int
}

func newOpCounter(t *testing.T) *opCounter {
	c := &opCounter{}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *opCounter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestAPIRoleVehiclesUnderPermitEnforce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	gw := newOpCounter(t)
	client := rivian.NewLive().WithEndpoint(gw.srv.URL)
	client.Restore(rivian.Session{UserSessionToken: "fake-user-session", CSRFToken: "fake-csrf", AppSessionToken: "fake-app", AuthenticatedAt: time.Now()})

	var refusedMu sync.Mutex
	var refused []string
	rivian.SetPermitMode(rivian.PermitEnforce)
	rivian.SetUnpermittedObserver(func(op string) {
		refusedMu.Lock()
		refused = append(refused, op)
		refusedMu.Unlock()
	})
	t.Cleanup(func() { rivian.SetPermitMode(rivian.PermitOff); rivian.SetUnpermittedObserver(nil) })
	refusals := func() int { refusedMu.Lock(); defer refusedMu.Unlock(); return len(refused) }

	// The api pod: a lease store that is never asked to lead, as the role leaves it (no coordinator).
	const apiPod = "api-role-pod"
	store, err := leases.NewStore(pool, apiPod)
	if err != nil {
		t.Fatal(err)
	}
	store.SetGroupLead(true)

	v := goldenVehicle()
	synced := v
	synced.InfoSynced = true
	syncedUser := acceptedUser(ctx, t, pool, "a3-synced")
	if err := db.UpsertVehicleFromSync(ctx, pool, syncedUser, db.VehicleSync{RivianID: v.ID + "-a3", VIN: v.VIN, Name: v.Name, Model: v.Model, ModelYear: v.ModelYear,
		Trim: v.TrimID, PackKWh: v.PackKWh, ImageURL: v.ImageURL, Info: synced.StoredInfo()}); err != nil {
		t.Fatal(err)
	}
	unsyncedUser := acceptedUser(ctx, t, pool, "a3-unsynced")
	if err := db.UpsertVehicleFromSync(ctx, pool, unsyncedUser, db.VehicleSync{RivianID: v.ID + "-a3u", VIN: v.VIN, Name: v.Name, Model: v.Model,
		ModelYear: v.ModelYear, Trim: v.TrimID, PackKWh: v.PackKWh}); err != nil {
		t.Fatal(err)
	}
	emptyUser := acceptedUser(ctx, t, pool, "a3-empty")

	list := func(uid uuid.UUID, discovery bool) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h := handleVehicles(client, nil, pool, nil, nil, nil, &vehiclesFromDB{account: linkedAccount, ownerDiscovery: discovery})
		req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil).WithContext(auth.WithUser(ctx, uid))
		h(rec, req)
		return rec
	}

	for name, tc := range map[string]struct {
		user      uuid.UUID
		wantInLst bool
	}{
		"owner has synced the rows":        {syncedUser, true},
		"rows stored, not yet synced":      {unsyncedUser, true},
		"nothing stored, owner is looking": {emptyUser, false},
	} {
		rec := list(tc.user, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", name, rec.Code, rec.Body)
		}
		var got []rivian.Vehicle
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || (len(got) > 0) != tc.wantInLst {
			t.Errorf("%s: body %s (%v), want vehicles=%v", name, rec.Body, err, tc.wantInLst)
		}
	}
	if n := gw.count(); n != 0 {
		t.Errorf("the api pod sent %d requests to Rivian, want 0", n)
	}
	if n := refusals(); n != 0 {
		t.Errorf("%d calls refused for want of a permit (%v), want 0", n, refused)
	}
	var leaseRows int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM account_leases WHERE pod_id = $1`, apiPod).Scan(&leaseRows); err != nil {
		t.Fatal(err)
	}
	if leaseRows != 0 {
		t.Errorf("the api pod holds %d leases, want 0", leaseRows)
	}

	// Negative control: without owner discovery an unsynced list asks Rivian (getUserInfo). The enforce permit refuses it before it is sent, which
	// is what permitEnforceEnvError exists to stop at boot, and it shows the counters above can move.
	rec := list(unsyncedUser, false)
	if rec.Code == http.StatusOK || refusals() == 0 || gw.count() != 0 {
		t.Errorf("control: status %d, refused %d, sent %d; want a refusal and nothing sent", rec.Code, refusals(), gw.count())
	}
}
