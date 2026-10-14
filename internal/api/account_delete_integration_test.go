//go:build integration

// Account deletion: signup_requests purge and admin-visible IdP
// deletion failures (architect A6).
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"context"
	"encoding/json"
	"errors"
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

	// Person signed back in through the surviving identity: refuse.
	again, err := db.EnsureUser(ctx, pool, "gone@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if rec := do(http.MethodPost, "/idp-delete-failures/"+f.ID.String()+"/retry"); rec.Code != http.StatusConflict {
		t.Errorf("retry with a re-created account = %d, want 409", rec.Code)
	}
	if err := db.DeleteUser(ctx, pool, again); err != nil {
		t.Fatal(err)
	}

	// IdP back: retry resolves and the row (with the email) is gone.
	provider.failing = false
	if rec := do(http.MethodPost, "/idp-delete-failures/"+f.ID.String()+"/retry"); rec.Code != http.StatusOK {
		t.Errorf("retry with IdP up = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if failures, _ = db.ListIdPDeleteFailures(ctx, pool); len(failures) != 0 {
		t.Errorf("failures after successful retry = %+v, want none", failures)
	}
	if rec := do(http.MethodPost, "/idp-delete-failures/"+uuid.NewString()+"/retry"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown failure id = %d, want 404", rec.Code)
	}
}
