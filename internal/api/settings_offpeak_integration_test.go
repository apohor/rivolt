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

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/testpg"
)

// The schedule round trips through the real settings row, clears with an empty
// window list, stays out of other users' rows, and goes with the account.
func TestOffpeakRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, err := db.EnsureUser(ctx, pool, "offpeak-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.EnsureUser(ctx, pool, "offpeak-b")
	if err != nil {
		t.Fatal(err)
	}
	sf := settings.NewFactory(pool)
	sa, sb := sf.For(a), sf.For(b)
	if err := settings.SetChargingConfig(ctx, sa, settings.ChargingConfig{HomePricePerKWh: 0.21, HomeCurrency: "USD"}); err != nil {
		t.Fatal(err)
	}
	do := func(h http.HandlerFunc, body string) (int, offpeakResponse) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPut, "/x", strings.NewReader(body)))
		var out offpeakResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := do(handleOffpeakPut(sa), `{"tz":"America/Chicago","windows":[{"days":"weekdays","start":"21:00","end":"06:00","rate":0.0912345,"label":"Overnight"}]}`)
	if code != 200 || !out.Configured || len(out.Windows) != 1 || out.Windows[0].Rate != 0.0912 || out.BaseRate != 0.21 || out.Currency != "USD" || out.TZ != "America/Chicago" {
		t.Fatalf("save: %d %+v", code, out)
	}
	// Another user sees none.
	rec := httptest.NewRecorder()
	handleOffpeakGet(sb)(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	var other offpeakResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &other)
	if other.Configured || len(other.Windows) != 0 {
		t.Errorf("another user's schedule leaked: %+v", other)
	}
	// It is in the settings export.
	all, _ := sa.GetAll(ctx)
	if _, ok := exportSettings(all)["charging.offpeak_schedule"]; !ok {
		t.Error("the schedule is not in the settings export")
	}
	// Clearing.
	if code, out = do(handleOffpeakPut(sa), `{"windows":[]}`); code != 200 || out.Configured || len(out.Windows) != 0 {
		t.Errorf("clear: %d %+v", code, out)
	}
	if all, _ = sa.GetAll(ctx); all["charging.offpeak_schedule"] != "" {
		t.Error("clearing left the key behind")
	}
	// Goes with the account.
	if _, code2 := do(handleOffpeakPut(sa), `{"tz":"America/Chicago","windows":[{"days":"all","start":"21:00","end":"06:00","rate":0.09}]}`); code2.Configured != true {
		t.Fatal("save before delete")
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM user_settings WHERE user_id = $1`, a).Scan(&n); err != nil || n != 0 {
		t.Errorf("settings rows left after the account was deleted: %d %v", n, err)
	}
}
