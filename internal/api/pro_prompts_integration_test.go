//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

// "Not now" against Postgres (Pro entry D6): one dismissal hides a prompt for 90 days, the second for good, a plan change brings it back,
// a read that fails hides everything, two writers do not lose a count, and deleting the account deletes the state.
func TestProPromptsAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUser(ctx, pool, "prompts-a")
	other, _ := db.EnsureUser(ctx, pool, "prompts-b")
	t0 := time.Date(2026, 12, 1, 9, 0, 0, 0, time.UTC)
	none := time.Time{}

	if h := hiddenPrompts(ctx, pool, uid, none, t0); len(h) != 0 {
		t.Fatalf("fresh account hides %v", h)
	}
	if _, err := dismissPrompt(ctx, pool, uid, "planner", none, t0); err != nil {
		t.Fatal(err)
	}
	if h := hiddenPrompts(ctx, pool, uid, none, t0.Add(24*time.Hour)); len(h) != 1 || h[0] != "planner" {
		t.Fatalf("after one Not now: %v", h)
	}
	if h := hiddenPrompts(ctx, pool, other, none, t0); len(h) != 0 {
		t.Fatalf("another account is affected: %v", h)
	}
	if h := hiddenPrompts(ctx, pool, uid, none, t0.Add(91*24*time.Hour)); len(h) != 0 {
		t.Fatalf("after 91 days it must show again: %v", h)
	}
	if m, err := dismissPrompt(ctx, pool, uid, "planner", none, t0.Add(91*24*time.Hour)); err != nil || m.Count != 2 {
		t.Fatalf("second Not now: %v %v", m, err)
	}
	if h := hiddenPrompts(ctx, pool, uid, none, t0.Add(1000*24*time.Hour)); len(h) != 1 {
		t.Fatalf("a second Not now hides for good: %v", h)
	}
	if m, _ := dismissPrompt(ctx, pool, uid, "planner", none, t0.Add(92*24*time.Hour)); m.Count != 2 {
		t.Fatalf("the count stops at 2, got %d", m.Count)
	}
	// A plan change after the dismissals brings the prompt back, and the next Not now starts over.
	lapse := t0.Add(200 * 24 * time.Hour)
	if h := hiddenPrompts(ctx, pool, uid, lapse, lapse.Add(time.Hour)); len(h) != 0 {
		t.Fatalf("a lapse must show the prompt again: %v", h)
	}
	if m, _ := dismissPrompt(ctx, pool, uid, "planner", lapse, lapse.Add(time.Hour)); m.Count != 1 {
		t.Fatalf("after a lapse the count starts over, got %d", m.Count)
	}

	// Two dismissals at the same moment (a double tap, two tabs) are one "Not now": hidden for 90 days, shown again after, not for good.
	v, _ := db.EnsureUser(ctx, pool, "prompts-c")
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := dismissPrompt(ctx, pool, v, "lapse_banner", none, t0); errs <- err }()
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if h := hiddenPrompts(ctx, pool, v, none, t0.Add(24*time.Hour)); len(h) != 1 {
		t.Fatalf("hidden the next day: %v", h)
	}
	if h := hiddenPrompts(ctx, pool, v, none, t0.Add(91*24*time.Hour)); len(h) != 0 {
		t.Fatalf("two simultaneous dismissals made it permanent: %v", h)
	}
	// A dismissal while it is hidden changes nothing, and does not move the 90 days either.
	if m, _ := dismissPrompt(ctx, pool, v, "lapse_banner", none, t0.Add(30*24*time.Hour)); m.Count != 1 || !m.DismissedAt.Equal(t0) {
		t.Fatalf("a dismissal of a hidden prompt changed it: %+v", m)
	}

	// A failed read hides every place.
	closed, _ := db.Open(ctx, testpg.DSN(ctx, t))
	_ = closed.Close()
	if h := hiddenPrompts(ctx, closed, uid, none, t0); len(h) != len(proPromptWheres) {
		t.Fatalf("a failed read must hide all, got %v", h)
	}

	// Deleting the account deletes the state.
	if err := db.DeleteUserAccount(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM user_settings WHERE user_id = $1 AND value ? $2`, uid, keyPromptState).Scan(&n); err != nil || n != 0 {
		t.Fatalf("prompt state left after account deletion: n=%d err=%v", n, err)
	}
	if h := hiddenPrompts(ctx, pool, other, none, t0); len(h) != 0 {
		t.Fatalf("deleting one account touched another: %v", h)
	}
}

// Through the real GET /api/auth/me handler and the real dismiss route over Postgres: the list of hidden prompts is what the page reads.
func TestProPromptsThroughMeAndRoute(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	free, _ := db.EnsureUser(ctx, pool, "pm-free")
	lapsed, _ := db.EnsureUser(ctx, pool, "pm-lapsed")
	pro, _ := db.EnsureUser(ctx, pool, "pm-pro")
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'active', NULL)`, pro); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end) VALUES ($1, 'grant', 'ended', NOW() - interval '1 hour')`, lapsed); err != nil {
		t.Fatal(err)
	}
	ent := entitlements.New(pool, entitlements.Config{Billing: true})
	d := Deps{DB: pool, Entitlements: ent}
	as := func(h func(uuid.UUID, http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			u, _ := uuid.Parse(r.Header.Get("X-Test-User"))
			h(u, w, r)
		}
	}
	rt := chi.NewRouter()
	rt.Post("/api/pro/prompts/{where}/dismiss", as(handleProPromptDismiss(d)))
	post := func(u uuid.UUID, where string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/pro/prompts/"+where+"/dismiss", nil)
		req.Header.Set("X-Test-User", u.String())
		rec := httptest.NewRecorder()
		rt.ServeHTTP(rec, req)
		return rec.Code
	}
	me := func(u uuid.UUID) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil).WithContext(auth.WithUser(ctx, u))
		rec := httptest.NewRecorder()
		handleMeEnrichedPlan(newMeAuth(t), pool, false, nil, false, ent, false, 0)(rec, req)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%v: %s", err, rec.Body.String())
		}
		return body
	}
	hidden := func(u uuid.UUID) []any {
		h, _ := me(u)["pro_prompts_hidden"].([]any)
		return h
	}

	if h := hidden(free); len(h) != 0 {
		t.Fatalf("fresh free account hides %v", h)
	}
	if c := post(free, "nowhere"); c != http.StatusNotFound {
		t.Fatalf("unknown place: %d, want 404", c)
	}
	if c := post(free, "planner"); c != http.StatusNoContent {
		t.Fatalf("dismiss: %d, want 204", c)
	}
	if h := hidden(free); len(h) != 1 || h[0] != "planner" {
		t.Fatalf("me after Not now: %v, want [planner]", h)
	}
	if c := post(free, "planner"); c != http.StatusNoContent {
		t.Fatalf("second tap: %d", c)
	}
	if h := hidden(lapsed); len(h) != 0 {
		t.Fatalf("another account is affected: %v", h)
	}
	// A lapsed account: a dismissal after the lapse hides the banner (a mark from before the lapse not counting is the unit test's case).
	if c := post(lapsed, "lapse_banner"); c != http.StatusNoContent {
		t.Fatalf("lapsed dismiss: %d", c)
	}
	if h := hidden(lapsed); len(h) != 1 || h[0] != "lapse_banner" {
		t.Fatalf("lapsed me after Not now: %v", h)
	}
	// A Pro account has no list.
	if _, has := me(pro)["pro_prompts_hidden"]; has {
		t.Fatal("a Pro account must not carry pro_prompts_hidden")
	}
}
