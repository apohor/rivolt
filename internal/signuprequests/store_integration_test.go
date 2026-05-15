//go:build integration

// signuprequests integration tests focused on the redemption flow.
// The store mints a single-use base32 token on approval; the public
// /api/signup/token/{token} endpoint reads it back, and /api/signup
// consumes it on account creation. Two adversarial paths matter most:
//
//   1. Double-redeem. Two concurrent POSTs to /api/signup with the
//      same token must NOT both succeed — the UPDATE … WHERE
//      token_used_at IS NULL is the single-row guarantee.
//   2. Expiry. A token whose expires_at has passed must look
//      identical to a missing/used one from the API (ErrTokenInvalid)
//      — leaking which state caused the rejection helps phishing.
//
// Run with: go test -tags=integration ./internal/signuprequests/...
package signuprequests_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/signuprequests"
)

func pgDSN(ctx context.Context, t *testing.T) string {
	t.Helper()
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	req := testcontainers.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "rivolt",
			"POSTGRES_PASSWORD": "rivolt",
			"POSTGRES_DB":       "rivolt_test",
		},
		WaitingFor: wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}
	return fmt.Sprintf("postgres://rivolt:rivolt@%s:%s/rivolt_test?sslmode=disable", host, port.Port())
}

func setupStore(t *testing.T) (*signuprequests.Store, uuid.UUID) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, pgDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	adminID, err := db.EnsureUser(ctx, pool, "signupadmin")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}
	return signuprequests.New(pool), adminID
}

// TestCreate_DuplicatePendingRejected pins the unique-on-pending
// constraint. Re-submitting the same email while a row is still
// pending must surface as ErrAlreadyPending so the frontend can show
// "we already received your request" instead of silently creating
// duplicate admin work.
func TestCreate_DuplicatePendingRejected(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()
	if _, err := store.Create(ctx, "user@example.com", "first"); err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	_, err := store.Create(ctx, "user@example.com", "second")
	if !errors.Is(err, signuprequests.ErrAlreadyPending) {
		t.Errorf("got %v want ErrAlreadyPending", err)
	}
	// Email is lower/trim-normalised.
	_, err = store.Create(ctx, "  USER@Example.com ", "third")
	if !errors.Is(err, signuprequests.ErrAlreadyPending) {
		t.Errorf("normalised-email got %v want ErrAlreadyPending", err)
	}
}

// TestApproveWithToken_HappyPath: pending → approved, with a fresh
// token + expiry stamped, and the row's SignupToken+TokenExpiresAt
// non-nil. Then LookupToken returns the same row.
func TestApproveWithToken_HappyPath(t *testing.T) {
	store, admin := setupStore(t)
	ctx := context.Background()
	created, err := store.Create(ctx, "approve@example.com", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	approved, err := store.ApproveWithToken(ctx, created.ID, admin, 0)
	if err != nil {
		t.Fatalf("ApproveWithToken: %v", err)
	}
	if approved.Status != signuprequests.StatusApproved {
		t.Errorf("status: %q", approved.Status)
	}
	if approved.SignupToken == nil || len(*approved.SignupToken) < 16 {
		t.Fatalf("token not minted: %v", approved.SignupToken)
	}
	if approved.TokenExpiresAt == nil || !approved.TokenExpiresAt.After(time.Now()) {
		t.Errorf("expires_at not in future: %v", approved.TokenExpiresAt)
	}
	if approved.DecidedBy == nil || *approved.DecidedBy != admin {
		t.Errorf("decided_by: %v want %v", approved.DecidedBy, admin)
	}

	got, err := store.LookupToken(ctx, *approved.SignupToken)
	if err != nil {
		t.Fatalf("LookupToken: %v", err)
	}
	if got.ID != approved.ID || got.Email != "approve@example.com" {
		t.Errorf("lookup row mismatch: %+v", got)
	}
}

// TestApproveWithToken_NotPending: re-approve / approve-after-reject
// surfaces ErrNotPending. The state machine is strict so the admin UI
// can't double-mint a token by impatiently clicking.
func TestApproveWithToken_NotPending(t *testing.T) {
	store, admin := setupStore(t)
	ctx := context.Background()
	r, _ := store.Create(ctx, "x@example.com", "")
	if _, err := store.ApproveWithToken(ctx, r.ID, admin, 0); err != nil {
		t.Fatalf("ApproveWithToken: %v", err)
	}
	_, err := store.ApproveWithToken(ctx, r.ID, admin, 0)
	if !errors.Is(err, signuprequests.ErrNotPending) {
		t.Errorf("got %v want ErrNotPending", err)
	}
}

// TestConsumeToken_SingleUse is the linchpin: two concurrent
// ConsumeToken calls with the same token must produce exactly one
// success and one ErrTokenInvalid. The UPDATE … WHERE token_used_at
// IS NULL is what gates this; a future refactor to a SELECT-then-
// UPDATE would break the guarantee and let a token mint two accounts.
func TestConsumeToken_SingleUse(t *testing.T) {
	store, admin := setupStore(t)
	ctx := context.Background()
	r, _ := store.Create(ctx, "single@example.com", "")
	approved, err := store.ApproveWithToken(ctx, r.ID, admin, 0)
	if err != nil {
		t.Fatalf("ApproveWithToken: %v", err)
	}
	token := *approved.SignupToken

	var (
		wg          sync.WaitGroup
		results     [2]error
	)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			<-start
			_, err := store.ConsumeToken(ctx, token)
			results[i] = err
		}()
	}
	close(start)
	wg.Wait()

	successes := 0
	for _, e := range results {
		if e == nil {
			successes++
		} else if !errors.Is(e, signuprequests.ErrTokenInvalid) {
			t.Errorf("unexpected error: %v", e)
		}
	}
	if successes != 1 {
		t.Errorf("concurrent consume: got %d successes, want 1 — single-use broken", successes)
	}

	// A third sequential consume must also be invalid.
	if _, err := store.ConsumeToken(ctx, token); !errors.Is(err, signuprequests.ErrTokenInvalid) {
		t.Errorf("post-consume reuse got %v want ErrTokenInvalid", err)
	}
	// And LookupToken now treats it as invalid (token_used_at non-null).
	if _, err := store.LookupToken(ctx, token); !errors.Is(err, signuprequests.ErrTokenInvalid) {
		t.Errorf("post-consume lookup got %v want ErrTokenInvalid", err)
	}
}

// TestConsumeToken_Expired pins the expiry side of the ErrTokenInvalid
// contract. The WHERE clause must reject a row whose token_expires_at
// has already passed — using a negative TTL forces that state without
// needing time travel.
func TestConsumeToken_Expired(t *testing.T) {
	store, admin := setupStore(t)
	ctx := context.Background()
	r, _ := store.Create(ctx, "expired@example.com", "")
	approved, err := store.ApproveWithToken(ctx, r.ID, admin, -1*time.Hour)
	if err != nil {
		t.Fatalf("ApproveWithToken (expired): %v", err)
	}
	token := *approved.SignupToken

	if _, err := store.LookupToken(ctx, token); !errors.Is(err, signuprequests.ErrTokenInvalid) {
		t.Errorf("expired lookup got %v want ErrTokenInvalid", err)
	}
	if _, err := store.ConsumeToken(ctx, token); !errors.Is(err, signuprequests.ErrTokenInvalid) {
		t.Errorf("expired consume got %v want ErrTokenInvalid", err)
	}
}

// TestLookupToken_RejectedAndEmpty: tokens in non-approved states (or
// blank/unknown values) surface uniformly as ErrTokenInvalid. The
// sentinel-uniformity is deliberate so the public endpoint can't be
// used to enumerate row states.
func TestLookupToken_RejectedAndEmpty(t *testing.T) {
	store, _ := setupStore(t)
	ctx := context.Background()
	// Empty + clearly-bogus.
	if _, err := store.LookupToken(ctx, ""); !errors.Is(err, signuprequests.ErrTokenInvalid) {
		t.Errorf("empty token: %v", err)
	}
	if _, err := store.LookupToken(ctx, "DOES-NOT-EXIST"); !errors.Is(err, signuprequests.ErrTokenInvalid) {
		t.Errorf("unknown token: %v", err)
	}
}

// TestReject_PendingOnly mirrors the Approve state-machine guard:
// rejecting a row that isn't pending returns ErrNotPending.
func TestReject_PendingOnly(t *testing.T) {
	store, admin := setupStore(t)
	ctx := context.Background()
	r, _ := store.Create(ctx, "reject@example.com", "")
	if _, err := store.Reject(ctx, r.ID, admin); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	_, err := store.Reject(ctx, r.ID, admin)
	if !errors.Is(err, signuprequests.ErrNotPending) {
		t.Errorf("second reject got %v want ErrNotPending", err)
	}
	// Approve-after-reject is rejected too.
	_, err = store.ApproveWithToken(ctx, r.ID, admin, 0)
	if !errors.Is(err, signuprequests.ErrNotPending) {
		t.Errorf("approve-after-reject got %v want ErrNotPending", err)
	}
	// Re-Create after reject is allowed — requester is welcome to retry.
	if _, err := store.Create(ctx, "reject@example.com", "second try"); err != nil {
		t.Errorf("post-reject Create: %v", err)
	}
}
