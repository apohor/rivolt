//go:build integration

// Account deletion: signup_requests purge and admin-visible IdP
// deletion failures (architect A6).
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/idp"
)

// flakyIdP fails DeleteUser while failing is true.
type flakyIdP struct {
	failing bool
	deleted []string
}

func (f *flakyIdP) Enabled() bool                                       { return true }
func (f *flakyIdP) CreateUser(context.Context, idp.CreateRequest) error { return nil }
func (f *flakyIdP) CreateUserGeneratePassword(context.Context, idp.CreateRequest) (string, error) {
	return "pw", nil
}
func (f *flakyIdP) DeleteUser(_ context.Context, username string) error {
	if f.failing {
		return errors.New("kratos: 503 service unavailable")
	}
	f.deleted = append(f.deleted, username)
	return nil
}

func TestAccountDelete_PurgesSignupRequestsAndRecordsIdPFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()

	admin, err := db.EnsureUser(ctx, pool, "admin@example.com") // first user: bootstrap admin
	if err != nil {
		t.Fatal(err)
	}
	target, err := db.EnsureUserFull(ctx, pool, "Gone@Example.com", "gone@example.com", "Gone")
	if err != nil {
		t.Fatal(err)
	}
	keep, err := db.EnsureUserFull(ctx, pool, "keep@example.com", "keep@example.com", "Keep")
	if err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"gone@example.com", "GONE@example.com", "keep@example.com"} {
		if _, err := pool.ExecContext(ctx,
			`INSERT INTO signup_requests (email, status) VALUES ($1, 'approved')`, email); err != nil {
			t.Fatalf("seed signup request %s: %v", email, err)
		}
	}
	countRequests := func(email string) int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM signup_requests WHERE lower(email) = lower($1)`, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The rollback path (DeleteUser) must leave signup requests alone.
	if err := db.DeleteUser(ctx, pool, keep); err != nil {
		t.Fatal(err)
	}
	if countRequests("keep@example.com") != 1 {
		t.Fatal("DeleteUser (rollback path) purged a live signup request")
	}

	// Admin deletes the account while the IdP is down.
	provider := &flakyIdP{failing: true}
	r := chi.NewRouter()
	r.Delete("/users/{id}", handleAdminUserDelete(pool, provider, nil))
	r.Get("/idp-delete-failures", handleAdminIdPFailures(pool))
	r.Post("/idp-delete-failures/{id}/retry", handleAdminIdPFailureRetry(pool, provider, nil))
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil).WithContext(auth.WithUser(ctx, admin))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodDelete, "/users/"+target.String())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"idp":"failed"`) ||
		!strings.Contains(rec.Body.String(), "503") {
		t.Fatalf("admin delete = %d %s, want 200 with idp=failed", rec.Code, rec.Body.String())
	}
	if n := countRequests("gone@example.com"); n != 0 {
		t.Errorf("signup requests for the deleted account = %d, want 0 (case-insensitive purge)", n)
	}
	if countRequests("keep@example.com") != 1 {
		t.Error("another person's signup request was purged")
	}

	rec = do(http.MethodGet, "/idp-delete-failures")
	var failures []db.IdPDeleteFailure
	if err := json.Unmarshal(rec.Body.Bytes(), &failures); err != nil || len(failures) != 1 {
		t.Fatalf("failures = %s (%v), want 1", rec.Body.String(), err)
	}
	f := failures[0]
	if f.Username != "gone@example.com" || f.Attempts != 1 || f.AccountExists || !strings.Contains(f.Error, "503") {
		t.Errorf("failure row = %+v", f)
	}

	// Retry while still failing: 502, attempts bumped.
	if rec := do(http.MethodPost, "/idp-delete-failures/"+f.ID.String()+"/retry"); rec.Code != http.StatusBadGateway {
		t.Errorf("retry while IdP down = %d, want 502", rec.Code)
	}
	failures, _ = db.ListIdPDeleteFailures(ctx, pool)
	if len(failures) != 1 || failures[0].Attempts != 2 {
		t.Errorf("after failed retry: %+v, want attempts 2", failures)
	}

	// Person signed back in through the surviving identity: refuse and
	// drop the failure (the identity is in use; nothing to clean up).
	again, err := db.EnsureUser(ctx, pool, "gone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(http.MethodPost, "/idp-delete-failures/"+f.ID.String()+"/retry"); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), `"dropped":true`) {
		t.Errorf("retry with a re-created account = %d %s, want 409 dropped", rec.Code, rec.Body.String())
	}
	if failures, _ = db.ListIdPDeleteFailures(ctx, pool); len(failures) != 0 {
		t.Errorf("failures after the 409 = %+v, want none", failures)
	}
	if err := db.DeleteUser(ctx, pool, again); err != nil {
		t.Fatal(err)
	}

	if rec := do(http.MethodPost, "/idp-delete-failures/"+uuid.NewString()+"/retry"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown failure id = %d, want 404", rec.Code)
	}
}

// The background retrier: per-row backoff, resolves when the IdP is back,
// and drops rows whose person signed back up (A13).
func TestIdPDeleteRetrier(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()
	provider := &flakyIdP{failing: true}

	due, err := db.RecordIdPDeleteFailure(ctx, pool, "due@example.com", "kratos: 503")
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := db.RecordIdPDeleteFailure(ctx, pool, "fresh@example.com", "kratos: 503")
	back, _ := db.RecordIdPDeleteFailure(ctx, pool, "back@example.com", "kratos: 503")
	if _, err := db.EnsureUser(ctx, pool, "back@example.com"); err != nil { // signed back up
		t.Fatal(err)
	}
	age := func(id uuid.UUID, d time.Duration) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, `UPDATE idp_delete_failures SET last_tried = NOW() - $2::interval WHERE id = $1`,
			id, fmt.Sprintf("%d seconds", int(d.Seconds()))); err != nil {
			t.Fatal(err)
		}
	}
	attempts := func(id uuid.UUID) int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT attempts FROM idp_delete_failures WHERE id = $1`, id).Scan(&n); err != nil {
			return -1 // gone
		}
		return n
	}
	age(due, 6*time.Minute)  // first backoff (5 min) elapsed
	age(back, 6*time.Minute) // due too, but the person is back
	// fresh: tried just now, not due

	retryIdPDeleteFailures(ctx, pool, provider, nil)
	if attempts(due) != 2 || attempts(fresh) != 1 || attempts(back) != -1 || len(provider.deleted) != 0 {
		t.Fatalf("after a failing pass: due %d fresh %d back %d deleted %v; want 2, 1, gone, none",
			attempts(due), attempts(fresh), attempts(back), provider.deleted)
	}

	// Second backoff is 10 min: not due after 6.
	age(due, 6*time.Minute)
	provider.failing = false
	retryIdPDeleteFailures(ctx, pool, provider, nil)
	if attempts(due) != 2 {
		t.Errorf("retried before its 10 min backoff: attempts %d", attempts(due))
	}
	// A row that has failed for months: 5 min × 2^59 would overflow an
	// interval and break the claim for every row; the exponent is capped.
	stuck, _ := db.RecordIdPDeleteFailure(ctx, pool, "stuck@example.com", "kratos: 503")
	if _, err := pool.ExecContext(ctx, `UPDATE idp_delete_failures SET attempts = 60, last_tried = NOW() - interval '25 hours' WHERE id = $1`, stuck); err != nil {
		t.Fatal(err)
	}
	age(due, 11*time.Minute)
	retryIdPDeleteFailures(ctx, pool, provider, nil)
	if attempts(stuck) != -1 {
		t.Errorf("row at attempts=60 not retried (attempts %d): the claim query must survive large exponents", attempts(stuck))
	}
	deleted := strings.Join(provider.deleted, ",")
	if attempts(due) != -1 || len(provider.deleted) != 2 || !strings.Contains(deleted, "due@example.com") ||
		!strings.Contains(deleted, "stuck@example.com") {
		t.Errorf("after the IdP came back: due %d deleted %v; want gone, due + stuck", attempts(due), provider.deleted)
	}
}

// Self-service deletion, end to end: the request must carry the confirmation; once it does, every session (a browser's and
// the app's), every push device (the phone's and a browser's) and the user row are gone in the one request, another
// account's rows are untouched, and the one log line says it happened without naming anyone.
func TestSelfDelete_RevokesEverythingAndLogsOnlyAHash(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()

	if _, err := db.EnsureUser(ctx, pool, "admin-self-delete@example.com"); err != nil { // the bootstrap admin, so the target is not the last one
		t.Fatal(err)
	}
	const email = "leaving-person@example.com"
	target, err := db.EnsureUserFull(ctx, pool, email, email, "Leaving Person")
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.EnsureUserFull(ctx, pool, "staying-person@example.com", "staying-person@example.com", "Stays")
	if err != nil {
		t.Fatal(err)
	}
	addSession := func(uid uuid.UUID, kind string) uuid.UUID {
		id := uuid.New()
		if _, err := pool.ExecContext(ctx, `INSERT INTO sessions (id, user_id, token_hash, expires_at, kind) VALUES ($1,$2,$3, now() + interval '1 day', $4)`,
			id, uid, []byte(id.String()), kind); err != nil {
			t.Fatal(err)
		}
		return id
	}
	webSess, appSess, otherApp := addSession(target, "web"), addSession(target, "app"), addSession(other, "app")
	addDevice := func(uid, sess uuid.UUID, tok string) {
		if _, err := pool.ExecContext(ctx, `INSERT INTO push_apns_devices (id, user_id, session_id, token, env, bundle_id) VALUES (gen_random_uuid(),$1,$2,$3,'sandbox','me.apoh.rivolt')`,
			uid, sess, tok); err != nil {
			t.Fatal(err)
		}
	}
	addDevice(target, appSess, strings.Repeat("a", 64))
	addDevice(other, otherApp, strings.Repeat("b", 64))
	for _, row := range []struct {
		uid      uuid.UUID
		endpoint string
	}{{target, "https://fcm.googleapis.com/fcm/send/leaving"}, {other, "https://fcm.googleapis.com/fcm/send/staying"}} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO push_subscriptions (user_id, endpoint, p256dh, auth) VALUES ($1,$2,'p','a')`, row.uid, row.endpoint); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		var n int
		if err := pool.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	var logs bytes.Buffer
	h := handleDataAccountDelete(Deps{DB: pool, Logger: slog.New(slog.NewJSONHandler(&logs, nil)), AuditKey: []byte("test-key")})
	send := func(body, ct string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "https://rivolt.dev/api/data/account", strings.NewReader(body))
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req = req.WithContext(auth.WithSession(auth.WithUser(ctx, target), auth.SessionRef{ID: webSess, Kind: "web"}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Without the confirmation nothing happens.
	for _, c := range [][2]string{{"", ""}, {`{"confirm":"no"}`, "application/json"}} {
		if rec := send(c[0], c[1]); rec.Code < 400 {
			t.Fatalf("a request without the confirmation: %d", rec.Code)
		}
	}
	if count(`SELECT count(*) FROM users WHERE id = $1`, target) != 1 {
		t.Fatal("the account went without the confirmation")
	}

	if rec := send(`{"confirm":"delete my account"}`, "application/json"); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body.String())
	}
	for what, q := range map[string]string{
		"the user":              `SELECT count(*) FROM users WHERE id = $1`,
		"its sessions":          `SELECT count(*) FROM sessions WHERE user_id = $1`,
		"its phone's devices":   `SELECT count(*) FROM push_apns_devices WHERE user_id = $1`,
		"its browser's devices": `SELECT count(*) FROM push_subscriptions WHERE user_id = $1`,
	} {
		if n := count(q, target); n != 0 {
			t.Errorf("%s: %d rows left", what, n)
		}
	}
	if count(`SELECT count(*) FROM sessions WHERE id = $1 OR id = $2`, webSess, appSess) != 0 {
		t.Error("a session of the deleted account survived (a token would still authenticate)")
	}
	// Another account is untouched.
	if count(`SELECT count(*) FROM sessions WHERE user_id = $1`, other) != 1 || count(`SELECT count(*) FROM push_apns_devices WHERE user_id = $1`, other) != 1 ||
		count(`SELECT count(*) FROM push_subscriptions WHERE user_id = $1`, other) != 1 {
		t.Error("another account's sessions or devices were touched")
	}
	// The one log line: a hash, the actor, no email, no id, no name.
	line := logs.String()
	if !strings.Contains(line, `"msg":"account deleted"`) || !strings.Contains(line, `"user_hash":"`+auditUserHash([]byte("test-key"), target)+`"`) || !strings.Contains(line, `"actor":"web"`) {
		t.Errorf("audit line: %s", line)
	}
	for _, leak := range []string{email, "Leaving", target.String(), strings.ReplaceAll(target.String(), "-", "")} {
		if strings.Contains(line, leak) {
			t.Errorf("the audit line names the person (%q): %s", leak, line)
		}
	}
}
