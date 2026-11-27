//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

// Behind the real plan lookup: a free account is refused 402 pro_required on both forecast radar routes, a Pro account is served.
func TestForecastRadarIsProOnlyAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "fr-free")
	pro, _ := db.EnsureUser(ctx, pool, "fr-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	h := forecastRadarMount((Deps{}).limiterSet(), &fakeForecast{}, entitlements.New(pool, entitlements.Config{Billing: true}))
	for name, c := range map[string]struct {
		uid  uuid.UUID
		want int
	}{"free": {free, http.StatusPaymentRequired}, "pro": {pro, http.StatusOK}} {
		for _, p := range []string{"/api/maps/forecast-radar/hours", "/api/maps/forecast-radar/2026112712/3/7/20/45.png"} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "https://rivolt.dev"+p, nil)
			req = req.WithContext(auth.WithUser(req.Context(), c.uid))
			h.ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("%s %s = %d, want %d: %s", name, p, rec.Code, c.want, rec.Body)
			}
			if name == "free" && !strings.Contains(rec.Body.String(), "pro_required") {
				t.Errorf("free body %s", rec.Body)
			}
		}
	}
}
