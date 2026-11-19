//go:build integration

// GET /api/admin/kill-switch against real Postgres: the flags the admin
// Operations page lists (money tab, statement email, AI call cap) read
// back as last set, next to the kill switch.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/flags"
)

func TestFlagsGet_ListsEveryFlagState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store, err := flags.OpenStore(ctx, pool, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatementEmail(ctx, flags.AudienceState{Users: []string{"u-1", "u-2"}}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAICallCap(ctx, 40, "admin"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	handleFlagsGet(store)(rec, httptest.NewRequest(http.MethodGet, "/api/admin/kill-switch", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		KillSwitch     *flags.KillSwitchState `json:"kill_switch"`
		StatementEmail *flags.AudienceState   `json:"statement_email"`
		AICallCap      *flags.AICallCapState  `json:"ai_call_cap"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.KillSwitch == nil {
		t.Error("kill_switch missing")
	}
	if strings.Contains(rec.Body.String(), `"money_tab"`) {
		t.Errorf("the retired money_tab flag is still reported: %s", rec.Body.String())
	}
	if got.StatementEmail == nil || got.StatementEmail.All || len(got.StatementEmail.Users) != 2 {
		t.Errorf("statement_email = %+v, want users u-1, u-2", got.StatementEmail)
	}
	if got.AICallCap == nil || got.AICallCap.DailyLimit != 40 || got.AICallCap.Actor != "admin" {
		t.Errorf("ai_call_cap = %+v, want 40 by admin", got.AICallCap)
	}
}

// PUT /api/admin/flags/live-destination writes the live_destination row as
// {"users":[...]} and the store, and a fresh Store on the same database,
// reads it back; all writes {"all":true}; an empty list turns it off.
func TestFlagsLiveDestinationPut_WritesTheRow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store, err := flags.OpenStore(ctx, pool, nil)
	if err != nil {
		t.Fatalf("flags.Open: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.ExecContext(context.Background(), `DELETE FROM flags WHERE name = 'live_destination'`)
	})

	put := func(body string) int {
		rec := httptest.NewRecorder()
		handleFlagsLiveDestinationPut(store).ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
		return rec.Code
	}
	const id = "3f2b8c1e-5a4d-4e6f-9a0b-1c2d3e4f5a6b"
	if code := put(`{"users":["` + strings.ToUpper(id) + `"]}`); code != http.StatusOK {
		t.Fatalf("put: %d", code)
	}
	var raw string
	if err := pool.QueryRowContext(ctx, `SELECT value::text FROM flags WHERE name = 'live_destination'`).Scan(&raw); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if strings.Contains(raw, `"all"`) || !strings.Contains(raw, id) {
		t.Errorf("row = %s, want users [%s] and no all", raw, id)
	}
	if !store.LiveDestination().Enabled(id) || store.LiveDestination().Enabled("someone-else") {
		t.Errorf("snapshot = %+v", store.LiveDestination())
	}
	if code := put(`{"all":true,"users":["` + id + `"]}`); code != http.StatusBadRequest || store.LiveDestination().All {
		t.Errorf("all with users: %d, %+v, want 400 and not applied", code, store.LiveDestination())
	}
	if code := put(`{"all":true}`); code != http.StatusOK || !store.LiveDestination().Enabled("someone-else") {
		t.Fatalf("all: %d, %+v", code, store.LiveDestination())
	}
	fresh, err := flags.OpenStore(ctx, pool, nil)
	if err != nil {
		t.Fatalf("flags.Open: %v", err)
	}
	if !fresh.LiveDestination().All || len(fresh.LiveDestination().Users) != 0 {
		t.Errorf("fresh store = %+v, want all with no users", fresh.LiveDestination())
	}
	if code := put(`{"users":[]}`); code != http.StatusOK || store.LiveDestination().Enabled(id) {
		t.Errorf("clear: %d, %+v", code, store.LiveDestination())
	}
}
