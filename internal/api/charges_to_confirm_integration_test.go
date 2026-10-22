//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
)

// /api/charges/to-confirm: the caller's open auto-detected charges (estimated and
// needs_confirmation), newest first, with exactly the keys /api/charges uses for them,
// the evidence cut down to the candidates' network names, and nothing that locates
// the user (the stored evidence and the charge both carry coordinates and places).
func TestChargesToConfirm(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "tc-a")
	b, _ := db.EnsureUser(ctx, pool, "tc-b")
	resolvers := db.NewVehicleResolverFactory(pool)
	cf := charges.NewFactory(pool, resolvers)
	sf := settings.NewFactory(pool)
	base := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Hour)
	evidence := `{"candidates":[{"network":"Electrify America","slug":"electrify_america","distance_m":41,"lat":37.77491,"lon":-122.41941},{"network":"EVgo","slug":"evgo","lat":37.8,"lon":-122.3}],"seen":{"lat":37.77491,"lon":-122.41941,"place":"1 Market St"}}`
	charge := func(uid uuid.UUID, id string, at time.Time, state string, cost float64) {
		t.Helper()
		c := charges.Charge{ID: id, VehicleID: "tc-rv", StartedAt: at, EndedAt: at.Add(30 * time.Minute),
			StartSoCPct: 20, EndSoCPct: 70, EnergyAddedKWh: 40, MaxPowerKW: 150, Lat: 37.77491, Lon: -122.41941,
			Cost: cost, Currency: "USD", PricePerKWh: cost / 40, FinalState: "charging_complete", Source: "live"}
		if state != "" {
			c.NetworkState = state
			c.NetworkEvidence = json.RawMessage(evidence)
		}
		if err := cf.For(uid).Upsert(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	charge(a, "tc-old", base, "needs_confirmation", 20)
	charge(a, "tc-est", base.Add(2*time.Hour), "estimated", 16)
	charge(a, "tc-done", base.Add(4*time.Hour), "confirmed", 18)
	charge(a, "tc-plain", base.Add(6*time.Hour), "", 12)
	charge(b, "tc-b", base.Add(time.Hour), "estimated", 8)

	get := func(uid uuid.UUID) (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/api/charges/to-confirm", nil).WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		handleChargesToConfirm(cf.For(uid), sf.For(uid), nil)(w, req)
		return w.Code, w.Body.String()
	}
	code, raw := get(a)
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || code != 200 {
		t.Fatalf("%d %v %s", code, err, raw)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want the two open ones: %s", len(rows), raw)
	}
	var id0, id1 string
	_ = json.Unmarshal(rows[0]["ID"], &id0)
	_ = json.Unmarshal(rows[1]["ID"], &id1)
	if id0 != "tc-est" || id1 != "tc-old" {
		t.Errorf("order = %s, %s, want newest first", id0, id1)
	}
	for _, r := range rows {
		keys := []string{}
		for k := range r {
			keys = append(keys, k)
		}
		if len(keys) != 7 {
			t.Errorf("keys = %v, want exactly ID StartedAt PricePerKWh Cost Currency network_state network_evidence", keys)
		}
		for _, k := range []string{"ID", "StartedAt", "PricePerKWh", "Cost", "Currency", "network_state", "network_evidence"} {
			if _, ok := r[k]; !ok {
				t.Errorf("missing key %s: %s", k, raw)
			}
		}
	}
	var ev struct {
		Candidates []map[string]string `json:"candidates"`
	}
	_ = json.Unmarshal(rows[0]["network_evidence"], &ev)
	if len(ev.Candidates) != 2 || len(ev.Candidates[0]) != 1 || ev.Candidates[0]["network"] != "Electrify America" || ev.Candidates[1]["network"] != "EVgo" {
		t.Errorf("evidence = %s", rows[0]["network_evidence"])
	}
	var cost float64
	_ = json.Unmarshal(rows[0]["Cost"], &cost)
	if cost != 16 {
		t.Errorf("cost = %v", cost)
	}
	for _, banned := range []string{"lat", "lon", "slug", "distance", "place", "address", "market st", "37.77", "-122.4", "seen", "tc-b"} {
		if strings.Contains(strings.ToLower(raw), banned) {
			t.Errorf("the payload carries %q: %s", banned, raw)
		}
	}
	// The other user sees only their own.
	if _, rawB := get(b); !strings.Contains(rawB, "tc-b") || strings.Contains(rawB, "tc-est") {
		t.Errorf("user b: %s", rawB)
	}
}
