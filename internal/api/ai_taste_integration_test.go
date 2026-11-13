//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/ai"
	"github.com/apohor/rivolt/internal/aitaste"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

func tasteAnswer() string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1000,"output_tokens":500}}`
}

// tasteRig is the free account's AI path end to end on Postgres: the middleware, the operator provider on a fake Anthropic, and the
// error mapping the two LLM handlers use.
type tasteRig struct {
	pool    *sql.DB
	ent     *entitlements.Service
	tastes  *aitaste.Store
	spend   *aitaste.Spend
	calls   atomic.Int64
	fail    atomic.Bool
	handler http.Handler
	inner0  http.Handler
}

func newTasteRig(t *testing.T, ceilingUSD float64) *tasteRig {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	r := &tasteRig{pool: pool, ent: entitlements.New(pool, entitlements.Config{Billing: true}),
		tastes: aitaste.NewStore(pool), spend: aitaste.NewSpend(pool, ceilingUSD)}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		r.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.fail.Load() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"x"}}`))
			return
		}
		_, _ = w.Write([]byte(tasteAnswer()))
	}))
	t.Cleanup(fake.Close)
	prov, err := ai.NewAnthropic(ai.AnthropicConfig{APIKey: "sk-test", Model: ai.OperatorModel, BaseURL: fake.URL, Operator: true})
	if err != nil {
		t.Fatal(err)
	}
	// The route's shape: budget reservation, then the taste mark, then an explicit-action handler that calls the model.
	inner := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if _, _, err := prov.Complete(ai.WithRoute(req.Context(), ai.RouteAdvice), "s", "u"); err != nil {
			if writeAITasteError(w, err) {
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	r.inner0 = inner
	r.handler = requireAITasteMW(r.ent, r.tastes, r.spend)(inner)
	return r
}

func (r *tasteRig) inner() http.Handler { return r.inner0 }

func (r *tasteRig) do(t *testing.T, uid uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/trips/plan/advice", nil).
		WithContext(ai.WithReservation(auth.WithUser(context.Background(), uid)))
	w := httptest.NewRecorder()
	r.handler.ServeHTTP(w, req)
	return w
}

func TestFreeTasteIsFiveAMonthAndTheSixthIs402(t *testing.T) {
	r := newTasteRig(t, 5)
	ctx := context.Background()
	free, _ := db.EnsureUser(ctx, r.pool, "taste-free")
	for i := 1; i <= aitaste.MonthlyQuota; i++ {
		if w := r.do(t, free); w.Code != http.StatusOK {
			t.Fatalf("answer %d: %d %s", i, w.Code, w.Body)
		}
	}
	w := r.do(t, free)
	if w.Code != http.StatusPaymentRequired {
		t.Fatalf("sixth: %d %s, want 402", w.Code, w.Body)
	}
	var body struct {
		Error   string `json:"error"`
		ResetAt string `json:"reset_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error != "ai_taste_used" {
		t.Fatalf("body %s", w.Body)
	}
	at, err := time.Parse(time.RFC3339, body.ResetAt)
	if err != nil || !at.Equal(aitaste.ResetAt(time.Now())) || at.Day() != 1 || at.Location() != time.UTC {
		t.Fatalf("reset_at %q is not the first of the next UTC month", body.ResetAt)
	}
	if got := r.calls.Load(); got != aitaste.MonthlyQuota {
		t.Fatalf("the model was called %d times, want %d: the refused call must not reach it", got, aitaste.MonthlyQuota)
	}
}

func TestProAccountIsNotMetered(t *testing.T) {
	r := newTasteRig(t, 5)
	ctx := context.Background()
	pro, _ := db.EnsureUser(ctx, r.pool, "taste-pro")
	if _, err := r.pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < aitaste.MonthlyQuota+3; i++ {
		if w := r.do(t, pro); w.Code != http.StatusOK {
			t.Fatalf("pro answer %d: %d %s", i, w.Code, w.Body)
		}
	}
	if n, _ := r.tastes.Used(ctx, pro, time.Now()); n != 0 {
		t.Fatalf("a Pro account took %d tastes", n)
	}
}

func TestProviderFailureRefundsTheTaste(t *testing.T) {
	r := newTasteRig(t, 5)
	ctx := context.Background()
	u, _ := db.EnsureUser(ctx, r.pool, "taste-refund")
	r.fail.Store(true)
	for i := 0; i < 3; i++ {
		if w := r.do(t, u); w.Code != http.StatusBadGateway {
			t.Fatalf("failing call %d: %d", i, w.Code)
		}
	}
	if n, _ := r.tastes.Used(ctx, u, time.Now()); n != 0 {
		t.Fatalf("used = %d after three provider failures, want 0 (refunded, never below 0)", n)
	}
	before, _ := r.spend.Today(ctx, time.Now())
	r.fail.Store(false)
	if w := r.do(t, u); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	if n, _ := r.tastes.Used(ctx, u, time.Now()); n != 1 {
		t.Fatalf("used = %d, want 1", n)
	}
	after, _ := r.spend.Today(ctx, time.Now())
	if d := after - before; d <= 0 || d >= aitaste.EstimateUSD {
		t.Fatalf("the answer moved the day's spend by %.6f: want its real cost, above 0 and under the %.2f hold", d, aitaste.EstimateUSD)
	}
}

func TestConcurrentTastesNeverPassTheQuota(t *testing.T) {
	r := newTasteRig(t, 100)
	ctx := context.Background()
	u, _ := db.EnsureUser(ctx, r.pool, "taste-race")
	var wg sync.WaitGroup
	var ok, refused atomic.Int64
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch r.do(t, u).Code {
			case http.StatusOK:
				ok.Add(1)
			case http.StatusPaymentRequired:
				refused.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != aitaste.MonthlyQuota || refused.Load() != 20-aitaste.MonthlyQuota {
		t.Fatalf("ok=%d refused=%d, want %d and %d", ok.Load(), refused.Load(), aitaste.MonthlyQuota, 20-aitaste.MonthlyQuota)
	}
	if got := r.calls.Load(); got != aitaste.MonthlyQuota {
		t.Fatalf("model calls = %d", got)
	}
}

func TestDailyCeilingFailsClosedAndBurnsNoTaste(t *testing.T) {
	// A ceiling of two holds: the third call of the day is refused before it touches any account's taste.
	r := newTasteRig(t, aitaste.EstimateUSD*2+0.001)
	ctx := context.Background()
	// Start from an empty day so the test is repeatable on a shared database.
	if _, err := r.pool.ExecContext(ctx, `DELETE FROM ai_taste_spend WHERE day = $1`, aitaste.DayStart(time.Now())); err != nil {
		t.Fatal(err)
	}
	a, _ := db.EnsureUser(ctx, r.pool, "taste-ceil-a")
	b, _ := db.EnsureUser(ctx, r.pool, "taste-ceil-b")
	if w := r.do(t, a); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	if w := r.do(t, b); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	spent, _ := r.spend.Today(ctx, time.Now())
	// Two real answers cost a fraction of the two holds, so the ceiling is not yet used: lift the day to just under it.
	if _, err := r.pool.ExecContext(ctx, `UPDATE ai_taste_spend SET micro_usd = $2 WHERE day = $1`, aitaste.DayStart(time.Now()), int64((aitaste.EstimateUSD*2+0.001-0.01)*1e6)); err != nil {
		t.Fatal(err)
	}
	_ = spent
	w := r.do(t, a)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("over the ceiling: %d %s, want 503", w.Code, w.Body)
	}
	if n, _ := r.tastes.Used(ctx, a, time.Now()); n != 1 {
		t.Fatalf("the refused call burned a taste: used = %d, want 1", n)
	}
}

func TestMissingStoresLeaveTheOperatorKeyClosed(t *testing.T) {
	r := newTasteRig(t, 5)
	ctx := context.Background()
	u, _ := db.EnsureUser(ctx, r.pool, "taste-nostore")
	for name, h := range map[string]http.Handler{
		"no taste store": requireAITasteMW(r.ent, nil, r.spend)(r.inner()),
		"no spend store": requireAITasteMW(r.ent, r.tastes, nil)(r.inner()),
	} {
		req := httptest.NewRequest(http.MethodPost, "/x", nil).WithContext(ai.WithReservation(auth.WithUser(ctx, u)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Errorf("%s: the operator key answered a free account", name)
		}
	}
	if r.calls.Load() != 0 {
		t.Fatalf("the model was called %d times", r.calls.Load())
	}
}

func TestTasteTablesAreUnderTheirRules(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	var rls bool
	if err := pool.QueryRowContext(ctx, `SELECT relrowsecurity FROM pg_class WHERE relname = 'ai_taste_usage'`).Scan(&rls); err != nil || !rls {
		t.Fatalf("ai_taste_usage: row-level security %v, err %v", rls, err)
	}
	var policies int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM pg_policies WHERE tablename = 'ai_taste_usage'`).Scan(&policies)
	if policies != 1 {
		t.Fatalf("ai_taste_usage has %d policies, want 1", policies)
	}
	// ON DELETE CASCADE: the taste goes with the account.
	u, _ := db.EnsureUser(ctx, pool, "taste-cascade")
	if ok, err := aitaste.NewStore(pool).Reserve(ctx, u, time.Now(), 5); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, u); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM ai_taste_usage WHERE user_id = $1`, u).Scan(&left)
	if left != 0 {
		t.Fatalf("%d taste rows outlived the account", left)
	}
	// The month is the UTC month: the same user in the next month starts again.
	u2, _ := db.EnsureUser(ctx, pool, "taste-month")
	s := aitaste.NewStore(pool)
	now := time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if ok, _ := s.Reserve(ctx, u2, now, 5); !ok {
			t.Fatalf("reserve %d refused", i)
		}
	}
	if ok, _ := s.Reserve(ctx, u2, now, 5); ok {
		t.Fatal("sixth reserve in November allowed")
	}
	if ok, _ := s.Reserve(ctx, u2, now.Add(2*time.Minute), 5); !ok {
		t.Fatal("first reserve in December refused")
	}
}
