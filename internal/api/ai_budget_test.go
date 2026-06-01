//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/aibudget"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/flags"
)

// TestAIBudget_TryConsumeCap covers the store half: a per-user daily
// counter that increments atomically and refuses once it would exceed
// the cap, and a non-positive cap that never charges.
func TestAIBudget_TryConsumeCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	uid, err := db.EnsureUser(ctx, pool, "budget-user")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	store := aibudget.New(pool)

	// Cap of 2: first two calls pass, the third is refused.
	for i := 1; i <= 2; i++ {
		allowed, used, err := store.TryConsume(ctx, uid, 2)
		if err != nil {
			t.Fatalf("TryConsume %d: %v", i, err)
		}
		if !allowed || used != i {
			t.Fatalf("call %d: allowed=%v used=%d, want allowed=true used=%d", i, allowed, used, i)
		}
	}
	allowed, used, err := store.TryConsume(ctx, uid, 2)
	if err != nil {
		t.Fatalf("TryConsume over cap: %v", err)
	}
	if allowed {
		t.Fatalf("third call allowed; want refused (used=%d)", used)
	}

	// Non-positive cap means unlimited: always allowed, nothing charged.
	if ok, _, err := store.TryConsume(ctx, uid, 0); err != nil || !ok {
		t.Fatalf("uncapped consume: ok=%v err=%v, want ok=true", ok, err)
	}
}

// TestAIBudgetMW_429OverCap covers the gate half end-to-end: with a
// cap of 1, the first request reaches the handler and the second is
// refused with 429 + Retry-After, while the inner handler is not hit.
func TestAIBudgetMW_429OverCap(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	uid, err := db.EnsureUser(ctx, pool, "budget-mw-user")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	fl, err := flags.OpenStore(ctx, pool, nil)
	if err != nil {
		t.Fatalf("flags.OpenStore: %v", err)
	}
	if err := fl.SetAICallCap(ctx, 1, "test"); err != nil {
		t.Fatalf("SetAICallCap: %v", err)
	}

	var hits int
	mw := requireAIBudgetMW(fl, aibudget.New(pool))
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))

	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/trips/plan/advice", nil).
			WithContext(auth.WithUser(ctx, uid))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	if w := do(); w.Code != http.StatusOK {
		t.Fatalf("first request: got %d, want 200", w.Code)
	}
	w := do()
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d, want 429", w.Code)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 response missing Retry-After header")
	}
	if hits != 1 {
		t.Fatalf("inner handler hit %d times, want 1 (the refused call must not reach it)", hits)
	}
}
