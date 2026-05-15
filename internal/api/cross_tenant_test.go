//go:build integration

// Cross-tenant API isolation. The architecture invariant from
// CLAUDE.md is "every read scoped by user_id" — this test catches
// the worst-case regression of that invariant by exercising the
// real handler (handleDrives + handleCharges) end-to-end against
// two independent users sharing the same Factory and DB pool.
//
// What a future refactor that drops the scoping would look like
// from here: user B's response would include user A's rows. The
// assertions below would fail loudly.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

func crossTenantDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "rivolt",
			"POSTGRES_PASSWORD": "rivolt",
			"POSTGRES_DB":       "rivolt_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port())
}

// TestAPI_CrossTenantIsolation_DrivesAndCharges seeds rows for two
// users, then hits handleDrives + handleCharges for each via the
// real auth.WithUser context. The two sets of IDs must be disjoint.
func TestAPI_CrossTenantIsolation_DrivesAndCharges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	uidA, err := db.EnsureUser(ctx, pool, "tenant-a")
	if err != nil {
		t.Fatalf("EnsureUser A: %v", err)
	}
	uidB, err := db.EnsureUser(ctx, pool, "tenant-b")
	if err != nil {
		t.Fatalf("EnsureUser B: %v", err)
	}

	resolvers := db.NewVehicleResolverFactory(pool)
	driveFactory := drives.NewFactory(pool, resolvers)
	chargeFactory := charges.NewFactory(pool, resolvers)
	settingsFactory := settings.NewFactory(pool)

	now := time.Now().UTC().Truncate(time.Second)
	seedDrive := func(store *drives.Store, id, vid string, started time.Time) {
		t.Helper()
		d := drives.Drive{
			ID: id, VehicleID: vid,
			StartedAt: started, EndedAt: started.Add(30 * time.Minute),
			StartSoCPct: 80, EndSoCPct: 70,
			DistanceMi: 25, EnergyUsedKWh: 8,
			Source: "live",
		}
		if err := store.Upsert(ctx, d); err != nil {
			t.Fatalf("seed drive %s: %v", id, err)
		}
	}
	seedCharge := func(store *charges.Store, id, vid string, started time.Time) {
		t.Helper()
		c := charges.Charge{
			ID: id, VehicleID: vid,
			StartedAt: started, EndedAt: started.Add(30 * time.Minute),
			EnergyAddedKWh: 50, FinalState: "charging_complete",
			Source: "live",
		}
		if err := store.Upsert(ctx, c); err != nil {
			t.Fatalf("seed charge %s: %v", id, err)
		}
	}

	seedDrive(driveFactory.For(uidA), "drive-A1", "rv-A", now.Add(-3*time.Hour))
	seedDrive(driveFactory.For(uidA), "drive-A2", "rv-A", now.Add(-2*time.Hour))
	seedDrive(driveFactory.For(uidB), "drive-B1", "rv-B", now.Add(-time.Hour))

	seedCharge(chargeFactory.For(uidA), "charge-A1", "rv-A", now.Add(-90*time.Minute))
	seedCharge(chargeFactory.For(uidB), "charge-B1", "rv-B", now.Add(-45*time.Minute))

	hitDrives := func(uid uuid.UUID) []driveResponse {
		t.Helper()
		h := handleDrives(driveFactory.For(uid), chargeFactory.For(uid), settingsFactory.For(uid))
		req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
		req = req.WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
		}
		var out []driveResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode drives: %v", err)
		}
		return out
	}
	hitCharges := func(uid uuid.UUID) []chargeResponse {
		t.Helper()
		h := handleCharges(chargeFactory.For(uid), settingsFactory.For(uid))
		req := httptest.NewRequest(http.MethodGet, "/api/charges", nil)
		req = req.WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		h(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("charges status: %d body=%s", w.Code, w.Body.String())
		}
		var out []chargeResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode charges: %v", err)
		}
		return out
	}

	drivesA := hitDrives(uidA)
	drivesB := hitDrives(uidB)
	chargesA := hitCharges(uidA)
	chargesB := hitCharges(uidB)

	idSet := func(items any) map[string]bool {
		m := map[string]bool{}
		switch v := items.(type) {
		case []driveResponse:
			for _, x := range v {
				m[x.ID] = true
			}
		case []chargeResponse:
			for _, x := range v {
				m[x.ID] = true
			}
		}
		return m
	}

	aDriveIDs := idSet(drivesA)
	bDriveIDs := idSet(drivesB)
	if !aDriveIDs["drive-A1"] || !aDriveIDs["drive-A2"] || aDriveIDs["drive-B1"] {
		t.Errorf("A's drives wrong: %v", aDriveIDs)
	}
	if !bDriveIDs["drive-B1"] || bDriveIDs["drive-A1"] || bDriveIDs["drive-A2"] {
		t.Errorf("B's drives wrong (cross-tenant leak): %v", bDriveIDs)
	}

	aChargeIDs := idSet(chargesA)
	bChargeIDs := idSet(chargesB)
	if !aChargeIDs["charge-A1"] || aChargeIDs["charge-B1"] {
		t.Errorf("A's charges wrong: %v", aChargeIDs)
	}
	if !bChargeIDs["charge-B1"] || bChargeIDs["charge-A1"] {
		t.Errorf("B's charges wrong (cross-tenant leak): %v", bChargeIDs)
	}
}

// TestAPI_NoUserContext_Unauthorized: hitting a withUser-wrapped
// route with no auth context returns 401. This is the secondary
// half of the isolation contract — the route must NOT silently
// run with uuid.Nil, which would either fail at the store layer
// or (worse) match nothing and look like an empty account.
func TestAPI_NoUserContext_Unauthorized(t *testing.T) {
	h := withUser(func(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
		t.Errorf("handler ran with uid=%s — must have been blocked", uid)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/drives", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}
