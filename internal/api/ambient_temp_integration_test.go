//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
)

func TestAmbientTempSettingDefaultsOffAndRoundTrips(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "ambient-user")
	if err != nil {
		t.Fatal(err)
	}
	sf := settings.NewFactory(pool)
	get := func() string {
		w := httptest.NewRecorder()
		handleAmbientTempGet(sf)(uid, w, httptest.NewRequest("GET", "/api/settings/ambient-temp", nil))
		return strings.TrimSpace(w.Body.String())
	}
	put := func(body string) int {
		w := httptest.NewRecorder()
		handleAmbientTempPut(sf)(uid, w, httptest.NewRequest("PUT", "/api/settings/ambient-temp", strings.NewReader(body)))
		return w.Code
	}
	if get() != `{"enabled":false}` {
		t.Fatalf("default = %s, want off", get())
	}
	if put(`{}`) != http.StatusBadRequest {
		t.Error("a body without enabled must be a 400")
	}
	if put(`{"enabled":true}`) != 200 || get() != `{"enabled":true}` {
		t.Fatalf("on: %s", get())
	}
	if put(`{"enabled":false}`) != 200 || get() != `{"enabled":false}` {
		t.Fatalf("off again: %s", get())
	}
	// It is independent of the drive-weather and AI settings.
	if settings.AIConsent(ctx, sf.For(uid)) {
		t.Error("turning ambient on or off changed the AI switch")
	}
}
