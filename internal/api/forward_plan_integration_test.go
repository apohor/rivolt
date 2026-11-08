//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/testpg"
)

// The Pro refusal comes before the forward: a free account asking for a multi-day plan gets 402 pro_required and nothing is forwarded; a
// Pro account's request reaches the planner and forwards its leg. The handler here is the planner call itself behind the real middleware.
func TestProRefusalMakesNoPlanForward(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "fwd-free")
	pro, _ := db.EnsureUser(ctx, pool, "fwd-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	ent := entitlements.New(pool, entitlements.Config{Billing: true})

	fw := &planForwarder{res: ServePlanLegs(ctx, plannerClient{Client: rivian.NewStub(), TripPlanner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) { return samplePlan(), nil })}, sampleInput())}
	planLeg := func(w http.ResponseWriter, r *http.Request) {
		uid, _ := auth.UserFromContext(r.Context())
		p := &forwardPlanner{inner: planFuncAPI(func(context.Context, rivian.PlanTripInput) (*rivian.TripPlan, error) {
			return &rivian.TripPlan{Status: "LOCAL"}, nil
		}),
			sf: &stateForward{fw: fw, user: uid, groupOf: func(string) (string, bool) { return "g", true }}}
		if _, err := p.PlanTrip(r.Context(), sampleInput()); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
	rt := chi.NewRouter()
	rt.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
		})
	})
	rt.With(multidayProMiddleware(ent)).Post("/trips/plan-multiday", planLeg)
	call := func(u uuid.UUID) int {
		req := httptest.NewRequest(http.MethodPost, "/trips/plan-multiday", strings.NewReader(`{"days":[{"end":{}},{"end":{}}]}`))
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := call(free); code != http.StatusPaymentRequired || fw.count() != 0 {
		t.Fatalf("free: status %d with %d forwards, want 402 and 0", code, fw.count())
	}
	if code := call(pro); code != http.StatusOK || fw.count() != 1 {
		t.Fatalf("pro: status %d with %d forwards, want 200 and 1", code, fw.count())
	}
}
