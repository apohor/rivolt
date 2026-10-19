//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
)

func TestAIConsentRoundTripAndGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUserVerified(ctx, pool, "ai-consent", "ai-consent@x.test", "ai-consent", true)
	if err != nil {
		t.Fatal(err)
	}
	sf := settings.NewFactory(pool)
	withUser := func(r *http.Request) *http.Request { return r.WithContext(auth.WithUser(ctx, uid)) }

	hits := 0
	gate := requireAIConsentMW(sf)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++ }))
	call := func() int {
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, withUser(httptest.NewRequest("POST", "/api/trips/plan/advice", nil)))
		return w.Code
	}
	get := func() string {
		w := httptest.NewRecorder()
		handleAIConsentGet(sf)(uid, w, withUser(httptest.NewRequest("GET", "/api/settings/ai-consent", nil)))
		return strings.TrimSpace(w.Body.String())
	}
	put := func(body string) int {
		w := httptest.NewRecorder()
		handleAIConsentPut(sf)(uid, w, withUser(httptest.NewRequest("PUT", "/api/settings/ai-consent", strings.NewReader(body))))
		return w.Code
	}

	if get() != `{"enabled":false}` || call() != http.StatusForbidden || hits != 0 {
		t.Fatalf("default must be off: %s hits=%d", get(), hits)
	}
	if put(`{}`) != http.StatusBadRequest {
		t.Error("missing enabled must be a 400")
	}
	if put(`{"enabled":true}`) != 200 || get() != `{"enabled":true}` || call() != 200 || hits != 1 {
		t.Fatalf("on: %s hits=%d", get(), hits)
	}
	if put(`{"enabled":false}`) != 200 || call() != http.StatusForbidden || hits != 1 {
		t.Fatalf("off again: hits=%d", hits)
	}
}
