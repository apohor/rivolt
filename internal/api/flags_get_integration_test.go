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
	if err := store.SetMoneyTab(ctx, flags.MoneyTabState{All: true}, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatementEmail(ctx, flags.MoneyTabState{Users: []string{"u-1", "u-2"}}, "admin"); err != nil {
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
		MoneyTab       *flags.MoneyTabState   `json:"money_tab"`
		StatementEmail *flags.MoneyTabState   `json:"statement_email"`
		AICallCap      *flags.AICallCapState  `json:"ai_call_cap"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.KillSwitch == nil {
		t.Error("kill_switch missing")
	}
	if got.MoneyTab == nil || !got.MoneyTab.All {
		t.Errorf("money_tab = %+v, want all", got.MoneyTab)
	}
	if got.StatementEmail == nil || got.StatementEmail.All || len(got.StatementEmail.Users) != 2 {
		t.Errorf("statement_email = %+v, want users u-1, u-2", got.StatementEmail)
	}
	if got.AICallCap == nil || got.AICallCap.DailyLimit != 40 || got.AICallCap.Actor != "admin" {
		t.Errorf("ai_call_cap = %+v, want 40 by admin", got.AICallCap)
	}
}
