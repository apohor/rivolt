//go:build integration

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

func openFunnelDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	return ctx, pool
}

func postJSON(t *testing.T, ctx context.Context, h http.HandlerFunc, uid uuid.UUID, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)).
		WithContext(auth.WithUser(ctx, uid))
	w := httptest.NewRecorder()
	h(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// funnelCount reads one funnel counter back through the registry.
func funnelCount(t *testing.T, m *metrics.Metrics, event string) float64 {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range fams {
		if f.GetName() != "rivolt_funnel_events_total" {
			continue
		}
		for _, s := range f.GetMetric() {
			for _, l := range s.GetLabel() {
				if l.GetName() == "event" && l.GetValue() == event {
					return s.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func vehicleRows(t *testing.T, ctx context.Context, pool *sql.DB, uid uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT COUNT(*) FROM vehicles WHERE user_id = $1`, uid).Scan(&n); err != nil {
		t.Fatalf("count vehicles: %v", err)
	}
	return n
}

// TestRivianLogin_EmptyAccountIsReported covers the "connected but no
// vehicle" user: Rivian accepts the sign-in yet the account has no
// vehicles (typically an Authorized Driver invite that was never
// accepted). The login must say so - vehicle_count 0 plus the
// rivian_no_vehicles funnel event - instead of looking like success.
// Once the vehicle appears, "Check again" must seed it without a
// sign-out/sign-in cycle.
func TestRivianLogin_EmptyAccountIsReported(t *testing.T) {
	ctx, pool := openFunnelDB(t)
	uid, err := db.EnsureUser(ctx, pool, "mark")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	store := secrets.New(pool, crypto.NoopSealer{})
	mock := rivian.NewMock()
	mock.SetVehicles(nil)
	reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient { return mock })
	m := metrics.New()

	login := handleRivianLogin(reg, store, nil, nil, pool, m, nil)
	code, body := postJSON(t, ctx, login, uid, rivianLoginReq{Email: "driver@example.com", Password: "pw"})
	if code != http.StatusOK {
		t.Fatalf("login status: got %d, want 200 (%v)", code, body)
	}
	if got := body["vehicle_count"]; got != float64(0) {
		t.Fatalf("vehicle_count = %v, want 0", got)
	}
	if got := funnelCount(t, m, funnelRivianNoVehicles); got != 1 {
		t.Fatalf("rivian_no_vehicles = %v, want 1", got)
	}
	if n := vehicleRows(t, ctx, pool, uid); n != 0 {
		t.Fatalf("vehicles rows = %d, want 0", n)
	}

	// The invite gets accepted in the Rivian app.
	mock.SetVehicles([]rivian.Vehicle{{ID: "veh-1", Name: "Blue", Model: "R1S"}})
	refresh := handleRivianRefreshVehicles(reg, store, pool, m, nil)
	code, body = postJSON(t, ctx, refresh, uid, nil)
	if code != http.StatusOK {
		t.Fatalf("refresh status: got %d, want 200 (%v)", code, body)
	}
	if got := body["vehicle_count"]; got != float64(1) {
		t.Fatalf("refresh vehicle_count = %v, want 1", got)
	}
	if n := vehicleRows(t, ctx, pool, uid); n != 1 {
		t.Fatalf("vehicles rows after refresh = %d, want 1", n)
	}
	if got := funnelCount(t, m, funnelRivianVehiclesFound); got != 1 {
		t.Fatalf("rivian_vehicles_found = %v, want 1", got)
	}
}

// TestRivianRefreshVehicles_RequiresConnection: "Check again" on an
// account that never signed in is a 409, not a silent zero.
func TestRivianRefreshVehicles_RequiresConnection(t *testing.T) {
	ctx, pool := openFunnelDB(t)
	uid, err := db.EnsureUser(ctx, pool, "nobody")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	store := secrets.New(pool, crypto.NoopSealer{})
	reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient { return rivian.NewMock() })

	code, _ := postJSON(t, ctx, handleRivianRefreshVehicles(reg, store, pool, nil, nil), uid, nil)
	if code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", code)
	}
}

// TestSetupNudge_TwoEmailsForAnyUnconnectedUser pins the sweep's
// eligibility: every no-vehicle user (onboarded or not) gets a first
// email once a day old and one follow-up after the gap, then nothing;
// connected and brand-new users get none.
func TestSetupNudge_TwoEmailsForAnyUnconnectedUser(t *testing.T) {
	ctx, pool := openFunnelDB(t)
	mk := func(name string, age time.Duration) uuid.UUID {
		t.Helper()
		uid, err := db.EnsureUserFull(ctx, pool, name, name+"@example.com", name)
		if err != nil {
			t.Fatalf("EnsureUserFull(%s): %v", name, err)
		}
		if _, err := pool.ExecContext(ctx,
			`UPDATE users SET created_at = now() - $2::interval, onboarding_completed = FALSE WHERE id = $1`,
			uid, pgInterval(age)); err != nil {
			t.Fatalf("age %s: %v", name, err)
		}
		return uid
	}
	stalled := mk("stalled", 48*time.Hour)
	connected := mk("connected", 48*time.Hour)
	mk("fresh", time.Hour)
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO vehicles (user_id, rivian_vehicle_id) VALUES ($1, 'veh-c')`, connected); err != nil {
		t.Fatalf("seed vehicle: %v", err)
	}

	const minAge, followUp = 24 * time.Hour, 6 * 24 * time.Hour
	due := func() []db.SetupNudge {
		t.Helper()
		out, err := db.ListUsersDueForSetupNudge(ctx, pool, minAge, followUp)
		if err != nil {
			t.Fatalf("ListUsersDueForSetupNudge: %v", err)
		}
		return out
	}
	claim := func(attempt int) {
		t.Helper()
		ok, err := db.ClaimSetupNudge(ctx, pool, stalled, attempt)
		if err != nil || !ok {
			t.Fatalf("ClaimSetupNudge(%d) = %v, %v; want true", attempt, ok, err)
		}
		if ok, _ := db.ClaimSetupNudge(ctx, pool, stalled, attempt); ok {
			t.Fatalf("ClaimSetupNudge(%d) won twice", attempt)
		}
	}

	got := due()
	if len(got) != 1 || got[0].UserID != stalled || got[0].Attempt != 1 {
		t.Fatalf("first sweep = %+v, want only stalled at attempt 1 (not onboarded must still qualify)", got)
	}
	claim(1)
	if got := due(); len(got) != 0 {
		t.Fatalf("right after first email = %+v, want none", got)
	}

	if _, err := pool.ExecContext(ctx,
		`UPDATE users SET setup_nudged_at = now() - interval '7 days' WHERE id = $1`, stalled); err != nil {
		t.Fatalf("age nudge: %v", err)
	}
	got = due()
	if len(got) != 1 || got[0].Attempt != 2 {
		t.Fatalf("follow-up sweep = %+v, want stalled at attempt 2", got)
	}
	claim(2)

	if _, err := pool.ExecContext(ctx,
		`UPDATE users SET setup_nudged_at = now() - interval '30 days' WHERE id = $1`, stalled); err != nil {
		t.Fatalf("age nudge: %v", err)
	}
	if got := due(); len(got) != 0 {
		t.Fatalf("after two emails = %+v, want none", got)
	}
}

func pgInterval(d time.Duration) string {
	return fmt.Sprintf("%d seconds", int(d.Seconds()))
}
