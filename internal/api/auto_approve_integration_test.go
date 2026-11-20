//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/signuprequests"
)

type linkMail struct {
	mu    sync.Mutex
	links map[string][]string
}

func (m *linkMail) send(_ context.Context, to, link string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.links == nil {
		m.links = map[string][]string{}
	}
	m.links[to] = append(m.links[to], link)
	return nil
}

// Email signup requests under the signup cap are approved at once and mailed
// a link; over the cap they wait for an admin; an address with an account or
// over its daily limit gets the same answer and no mail; the cap is checked
// again when an automatic link is used, and an admin's link is exempt.
func TestSignupRequestAutoApprove(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	if _, err := pool.ExecContext(ctx, `DELETE FROM signup_requests`); err != nil {
		t.Fatal(err)
	}
	store := signuprequests.New(pool)
	mail := &linkMail{}
	var wg sync.WaitGroup
	limit := 2
	auto := &autoApprove{
		capOK: func(ctx context.Context) (bool, error) {
			n, err := db.CountUsers(ctx, pool)
			return limit > 0 && n < limit, err
		},
		userExists: func(ctx context.Context, e string) (bool, error) { return db.UserExistsByUsername(ctx, pool, e) },
		send:       mail.send,
		wg:         &wg,
		baseURL:    "https://rivolt.test",
	}
	h := handleSignupRequestCreate(store, nil, nil, auto)
	post := func(addr string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/signup/request", strings.NewReader(`{"email":"`+addr+`"}`))
		h(rec, req)
		wg.Wait() // the mail is sent in the background
		var b map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return rec.Code, b
	}
	status := func(addr string) (st string, byAdmin bool) {
		var by *uuid.UUID
		if err := pool.QueryRowContext(ctx, `SELECT status, decided_by FROM signup_requests WHERE email = $1 ORDER BY requested_at DESC LIMIT 1`, addr).Scan(&st, &by); err != nil {
			t.Fatal(err)
		}
		return st, by != nil
	}

	// Under the cap: approved, mailed, decided_by NULL.
	if code, b := post("a@example.com"); code != 201 || b["auto_approved"] != true {
		t.Fatalf("under cap: %d %v", code, b)
	}
	if st, by := status("a@example.com"); st != "approved" || by {
		t.Fatalf("a: status %q admin-decided %v", st, by)
	}
	if l := mail.links["a@example.com"]; len(l) != 1 || !strings.HasPrefix(l[0], "https://rivolt.test/signup?token=") {
		t.Fatalf("link mail = %v", mail.links)
	}

	// An address with an account: same answer, no mail, no row.
	if _, err := db.EnsureUser(ctx, pool, "b@example.com"); err != nil {
		t.Fatal(err)
	}
	if code, b := post("b@example.com"); code != 201 || b["auto_approved"] != true || len(mail.links["b@example.com"]) != 0 {
		t.Fatalf("existing account: %d %v mail=%v", code, b, mail.links["b@example.com"])
	}

	// Throttle: three links per address per day, then silence with the same answer.
	for i := 0; i < 5; i++ {
		post("c@example.com")
	}
	if n := len(mail.links["c@example.com"]); n != 3 {
		t.Errorf("links to one address = %d, want 3", n)
	}

	// Over the cap (2 users now): the request is created pending and nothing is mailed.
	if _, err := db.EnsureUser(ctx, pool, "d-filler@example.com"); err != nil {
		t.Fatal(err)
	}
	code, b := post("d@example.com")
	if code != 201 || b["auto_approved"] != nil || len(mail.links["d@example.com"]) != 0 {
		t.Fatalf("over cap: %d %v mail=%v", code, b, mail.links["d@example.com"])
	}
	if st, _ := status("d@example.com"); st != "pending" {
		t.Fatalf("over cap status = %q, want pending", st)
	}

	// Room opens up: the same person asks again and the pending request is approved.
	limit = 100
	if code, b := post("d@example.com"); code != 201 || b["auto_approved"] != true || len(mail.links["d@example.com"]) != 1 {
		t.Fatalf("re-request with room: %d %v mail=%v", code, b, mail.links["d@example.com"])
	}
	if st, _ := status("d@example.com"); st != "approved" {
		t.Fatalf("pending request not approved: %q", st)
	}
}

// Using an automatic link re-checks the cap; an admin-approved link does not.
func TestSignupTokenCapRecheck(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	if _, err := pool.ExecContext(ctx, `DELETE FROM signup_requests`); err != nil {
		t.Fatal(err)
	}
	store := signuprequests.New(pool)
	admin, err := db.EnsureUser(ctx, pool, "the-admin")
	if err != nil {
		t.Fatal(err)
	}
	mk := func(addr string, by uuid.UUID) string {
		t.Helper()
		r, err := store.Create(ctx, addr, "")
		if err != nil {
			t.Fatal(err)
		}
		row, err := store.ApproveWithToken(ctx, r.ID, by, 0)
		if err != nil {
			t.Fatal(err)
		}
		return *row.SignupToken
	}
	autoTok, adminTok := mk("auto@example.com", uuid.Nil), mk("byadmin@example.com", admin)
	atCap := func(context.Context) (bool, error) { return false, nil }
	call := func(tok string) int {
		rec := httptest.NewRecorder()
		handleSignup(pool, store, nil, nil, nil, nil, atCap)(rec, httptest.NewRequest("POST", "/api/signup",
			strings.NewReader(`{"signup_token":"`+tok+`","password":"Correct-Horse-9-Battery!"}`)))
		return rec.Code
	}
	if code := call(autoTok); code != http.StatusConflict {
		t.Errorf("auto link at the cap = %d, want 409", code)
	}
	if exists, _ := db.UserExistsByUsername(ctx, pool, "auto@example.com"); exists {
		t.Error("account created over the cap")
	}
	// The admin's link works at the cap. (No IdP configured: the row is created
	// and the response is 201.)
	if code := call(adminTok); code != http.StatusCreated {
		t.Errorf("admin link at the cap = %d, want 201", code)
	}
	_ = time.Second
}

// The whole email sign-up path once: request, automatic approval, the link in
// the mail, the lookup the SPA makes on it, the sign-up itself, and the same
// link afterwards. Each lookup and mail is counted by result.
func TestSignupEmailPathEndToEnd(t *testing.T) {
	ctx, pool := openGateDB(t)
	cleanUsers(t, ctx, pool)
	if _, err := pool.ExecContext(ctx, `DELETE FROM signup_requests`); err != nil {
		t.Fatal(err)
	}
	store := signuprequests.New(pool)
	mail := &linkMail{}
	var wg sync.WaitGroup
	m := metrics.New()
	auto := &autoApprove{
		capOK:      func(context.Context) (bool, error) { return true, nil },
		userExists: func(ctx context.Context, e string) (bool, error) { return db.UserExistsByUsername(ctx, pool, e) },
		send:       mail.send,
		wg:         &wg,
		baseURL:    "https://rivolt.test",
		metrics:    m,
	}
	r := chi.NewRouter()
	r.Post("/api/signup/request", handleSignupRequestCreate(store, nil, nil, auto))
	r.Get("/api/signup/token/{token}", handleSignupTokenLookup(store, m, nil))
	r.Post("/api/signup", handleSignup(pool, store, nil, nil, nil, nil, func(context.Context) (bool, error) { return true, nil }))
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		wg.Wait()
		return rec
	}

	if rec := do("POST", "/api/signup/request", `{"email":"path@example.com"}`); rec.Code != http.StatusCreated {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	links := mail.links["path@example.com"]
	if len(links) != 1 {
		t.Fatalf("mails: %v", mail.links)
	}
	token := strings.TrimPrefix(links[0], "https://rivolt.test/signup?token=")
	if token == links[0] || token == "" {
		t.Fatalf("link has no token: %q", links[0])
	}

	rec := do("GET", "/api/signup/token/"+token, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "path@example.com") {
		t.Fatalf("lookup: %d %s", rec.Code, rec.Body)
	}
	if rec := do("POST", "/api/signup", `{"signup_token":"`+token+`","password":"Correct-Horse-9-Battery!"}`); rec.Code != http.StatusCreated {
		t.Fatalf("signup: %d %s", rec.Code, rec.Body)
	}
	if exists, _ := db.UserExistsByUsername(ctx, pool, "path@example.com"); !exists {
		t.Error("no account after sign-up")
	}
	if rec := do("GET", "/api/signup/token/"+token, ""); rec.Code != http.StatusGone {
		t.Errorf("used link: %d, want 410", rec.Code)
	}
	if rec := do("GET", "/api/signup/token/nope", ""); rec.Code != http.StatusGone {
		t.Errorf("unknown link: %d, want 410", rec.Code)
	}

	for label, want := range map[string]float64{"ok": 1, "used": 1, "not_found": 1, "error": 0} {
		if got := testutil.ToFloat64(m.SignupTokenLookupsTotal.WithLabelValues(label)); got != want {
			t.Errorf("lookups %s = %v, want %v", label, got, want)
		}
	}
	if got := testutil.ToFloat64(m.SignupMailTotal.WithLabelValues("auto", "sent")); got != 1 {
		t.Errorf("auto mails sent = %v, want 1", got)
	}
}
