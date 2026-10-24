//go:build integration

// Sign in with Apple revocation on account deletion (SIWA C1; DA conditions): revoke before the cascade, or queue
// the token sealed so it still opens after the cascade; retried, then expired; deletion stops only when both fail.
//
// Run with: go test -tags=integration ./internal/api/...
package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/appleid"
	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/secrets"
)

// fakeAppleRevoke records revoked tokens and fails while failing is set.
type fakeAppleRevoke struct {
	failing bool
	revoked []string
}

func (f *fakeAppleRevoke) revoke(_ context.Context, tok string) error {
	if f.failing {
		return errors.New("apple revoke: 503 Service Unavailable")
	}
	f.revoked = append(f.revoked, tok)
	return nil
}

// failingSealer can't seal (the "both fail" case).
type failingSealer struct{ crypto.Sealer }

func (failingSealer) Seal(context.Context, uuid.UUID, []byte) ([]byte, error) {
	return nil, errors.New("sealer down")
}

func TestAppleRevokeOnAccountDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer pool.Close()

	sealer, err := crypto.NewEnvSealer("t1", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	links := appleid.New(pool, secrets.New(pool, sealer))
	admin, err := db.EnsureUser(ctx, pool, "admin@example.com") // first user: bootstrap admin
	if err != nil {
		t.Fatal(err)
	}
	newAppleUser := func(name, sub, tok string) uuid.UUID {
		t.Helper()
		uid, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := links.Link(ctx, uid, sub, tok); err != nil {
			t.Fatal(err)
		}
		return uid
	}
	var logs bytes.Buffer
	apple := &fakeAppleRevoke{}
	ar := &AppleRevocation{DB: pool, Token: links.RefreshToken, Revoke: apple.revoke, Sealer: sealer,
		Log: slog.New(slog.NewTextHandler(&logs, nil))}
	r := chi.NewRouter()
	r.Delete("/users/{id}", handleAdminUserDelete(pool, nil, ar, nil))
	r.Get("/idp-delete-failures", handleAdminIdPFailures(pool))
	del := func(uid uuid.UUID) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/users/"+uid.String(), nil).WithContext(auth.WithUser(ctx, admin))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}
	accountExists := func(uid uuid.UUID) bool {
		var ok bool
		if err := pool.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM users WHERE id = $1)`, uid).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	appleRows := func() int {
		var n int
		if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM idp_delete_failures WHERE kind = 'apple'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// 1. Apple reachable: revoked before the cascade, nothing queued.
	a := newAppleUser("a@example.com", "sub-a", "rt-a")
	if rec := del(a); rec.Code != http.StatusOK {
		t.Fatalf("delete a = %d %s", rec.Code, rec.Body.String())
	}
	if len(apple.revoked) != 1 || apple.revoked[0] != "rt-a" || accountExists(a) || appleRows() != 0 {
		t.Fatalf("a: revoked %v, exists %v, queued %d", apple.revoked, accountExists(a), appleRows())
	}

	// 2. Apple down: the deletion still completes, and the token is queued sealed.
	apple.failing = true
	b := newAppleUser("b@example.com", "sub-b", "rt-b-secret")
	if rec := del(b); rec.Code != http.StatusOK {
		t.Fatalf("delete b = %d %s", rec.Code, rec.Body.String())
	}
	if accountExists(b) || appleRows() != 1 {
		t.Fatalf("b: exists %v, queued %d; want deleted and 1 queued", accountExists(b), appleRows())
	}
	var sealed []byte
	var username string
	if err := pool.QueryRowContext(ctx, `SELECT sealed, username FROM idp_delete_failures WHERE kind = 'apple'`).Scan(&sealed, &username); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("rt-b-secret")) || username != "apple:"+b.String() {
		t.Errorf("queued row: token in the clear %v, username %q", bytes.Contains(sealed, []byte("rt-b-secret")), username)
	}
	// The Kratos side never sees Apple rows: not in the admin list, not claimed by its retrier.
	list := httptest.NewRecorder()
	r.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/idp-delete-failures", nil))
	if strings.TrimSpace(list.Body.String()) != "[]" {
		t.Errorf("admin IdP list = %s, want no Apple rows", list.Body.String())
	}
	if _, err := pool.ExecContext(ctx, `UPDATE idp_delete_failures SET last_tried = NOW() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if got, err := db.ClaimDueIdPDeleteFailures(ctx, pool, 10); err != nil || len(got) != 0 {
		t.Errorf("Kratos claim = %v, %v; want none", got, err)
	}

	// Retry while Apple is still down: attempts counted, row kept.
	ar.retryOnce(ctx)
	var attempts int
	if err := pool.QueryRowContext(ctx, `SELECT attempts FROM idp_delete_failures WHERE kind = 'apple'`).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("attempts = %d, %v; want 2", attempts, err)
	}
	// Apple back: the retry opens the token after the cascade (user_secrets is gone) and revokes it.
	apple.failing = false
	if _, err := pool.ExecContext(ctx, `UPDATE idp_delete_failures SET last_tried = NOW() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	ar.retryOnce(ctx)
	if len(apple.revoked) != 2 || apple.revoked[1] != "rt-b-secret" || appleRows() != 0 {
		t.Fatalf("after retry: revoked %v, queued %d", apple.revoked, appleRows())
	}

	// 3. Revoke and queue both fail: nothing is deleted.
	apple.failing = true
	c := newAppleUser("c@example.com", "sub-c", "rt-c")
	ar.Sealer = failingSealer{sealer}
	if rec := del(c); rec.Code != http.StatusInternalServerError || !accountExists(c) {
		t.Fatalf("both fail: delete = %d, exists %v; want 500 and the account kept", rec.Code, accountExists(c))
	}
	ar.Sealer = sealer

	// 4. Retention: a queued revoke goes after 30 days or 20 attempts, whichever first; only a count is logged.
	for i, q := range []string{
		`UPDATE idp_delete_failures SET failed_at = NOW() - interval '31 days' WHERE kind = 'apple'`,
		`UPDATE idp_delete_failures SET attempts = 20 WHERE kind = 'apple'`,
	} {
		if rec := del(c); rec.Code != http.StatusOK || appleRows() != 1 {
			t.Fatalf("case %d: delete c = %d, queued %d", i, rec.Code, appleRows())
		}
		if _, err := pool.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
		ar.retryOnce(ctx)
		if appleRows() != 0 {
			t.Fatalf("case %d: expired row still queued", i)
		}
		c = newAppleUser("c@example.com", "sub-c", "rt-c")
	}
	if !strings.Contains(logs.String(), "count=1") {
		t.Errorf("no expiry count logged:\n%s", logs.String())
	}

	// 5. Cancel (the purge's re-check found the account still there) drops the queued row.
	if rec := del(c); rec.Code != http.StatusOK || appleRows() != 1 {
		t.Fatalf("delete c again = %d, queued %d", rec.Code, appleRows())
	}
	ar.Cancel(ctx, c)
	if appleRows() != 0 {
		t.Error("Cancel left the queued revoke")
	}

	// The schema refuses an Apple row without its sealed token, or keyed by anything but its account id.
	if _, err := pool.ExecContext(ctx, `INSERT INTO idp_delete_failures (username, error, kind) VALUES ('apple:x', 'e', 'apple')`); err == nil {
		t.Error("an Apple row without a sealed token was accepted")
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO idp_delete_failures (username, error, kind, sealed, seal_uid) VALUES ('someone@example.com', 'e', 'apple', '\x01', $1)`, uuid.New()); err == nil {
		t.Error("an Apple row keyed by an email was accepted")
	}

	for _, s := range []string{"rt-a", "rt-b-secret", "rt-c", "sub-a", "sub-b", "sub-c", "@"} {
		if strings.Contains(logs.String(), s) {
			t.Errorf("logs contain %q:\n%s", s, logs.String())
		}
	}
}
