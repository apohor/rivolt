//go:build integration

// /api/export against real Postgres: isolation between users, the
// effective classification columns, no secrets, works without the
// Money tab flag.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
)

func TestExport_UserScopedNoSecretsAndEffectiveColumns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	alice, err := db.EnsureUser(ctx, pool, "export-alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := db.EnsureUser(ctx, pool, "export-bob")
	if err != nil {
		t.Fatal(err)
	}
	resolvers := db.NewVehicleResolverFactory(pool)
	cf := charges.NewFactory(pool, resolvers)
	df := drives.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)
	now := time.Now().UTC().Truncate(time.Second)

	if err := cf.For(alice).Upsert(ctx, charges.Charge{ID: "charge-alice", VehicleID: "rv-a", StartedAt: now.Add(-3 * time.Hour),
		EndedAt: now.Add(-2 * time.Hour), EnergyAddedKWh: 40, Cost: 6.4, Currency: "USD", CostSource: "home_rate", Source: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := cf.For(bob).Upsert(ctx, charges.Charge{ID: "charge-bob", VehicleID: "rv-b", StartedAt: now.Add(-3 * time.Hour),
		EndedAt: now.Add(-2 * time.Hour), EnergyAddedKWh: 99, Source: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := df.For(alice).Upsert(ctx, drives.Drive{ID: "drive-alice", VehicleID: "rv-a", StartedAt: now.Add(-5 * time.Hour),
		EndedAt: now.Add(-4 * time.Hour), DistanceMi: 20, Source: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := df.For(bob).Upsert(ctx, drives.Drive{ID: "drive-bob", VehicleID: "rv-b", StartedAt: now.Add(-5 * time.Hour),
		EndedAt: now.Add(-4 * time.Hour), DistanceMi: 99, Source: "live"}); err != nil {
		t.Fatal(err)
	}
	st := sf.For(alice)
	if err := st.Set(ctx, "charging.home_currency", "USD"); err != nil {
		t.Fatal(err)
	}
	if err := st.Set(ctx, "ai.openai.api_key", "sk-should-never-export"); err != nil {
		t.Fatal(err)
	}

	d := Deps{DB: pool, Charges: cf, Drives: df, Settings: sf, Version: "test"}
	get := func(format string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/export"+format, nil)
		req = req.WithContext(auth.WithUser(ctx, alice))
		w := httptest.NewRecorder()
		handleExport(d)(w, req)
		return w
	}
	w := get("")
	if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("zip: %d %s", w.Code, w.Header().Get("Content-Type"))
	}
	files := readZip(t, w.Body.Bytes())
	rows, err := csv.NewReader(strings.NewReader(files["charges.csv"])).ReadAll()
	if err != nil || len(rows) != 2 || rows[1][0] != "charge-alice" {
		t.Fatalf("charges.csv rows = %v (%v)", rows, err)
	}
	if strings.Contains(files["drives.csv"], "drive-bob") || !strings.Contains(files["drives.csv"], "drive-alice") {
		t.Fatalf("drives.csv leaked or empty: %s", files["drives.csv"])
	}
	for name, body := range files {
		if strings.Contains(body, "sk-should-never-export") || strings.Contains(body, "charge-bob") {
			t.Fatalf("%s leaks: %s", name, body)
		}
	}
	if !strings.Contains(files["settings.json"], "charging.home_currency") {
		t.Fatalf("settings.json missing preferences: %s", files["settings.json"])
	}
	if j := get("?format=json"); j.Code != http.StatusOK || strings.Contains(j.Body.String(), "sk-should-never-export") {
		t.Fatalf("json: %d", j.Code)
	}
	if bad := get("?format=xml"); bad.Code != http.StatusBadRequest {
		t.Fatalf("bad format = %d", bad.Code)
	}
}
