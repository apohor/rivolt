//go:build integration

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// TestRivianStatus_RehydratesSessionAcrossPods reproduces Scott's
// "follow the screens and it loops" report. The per-user client is
// cached per pod and only restores its session when first built. A
// pod that built the client before the user finished signing in on a
// peer pod keeps answering "not connected" from stale memory, so the
// SPA's status poll disagrees pod-to-pod and the login screen loops.
//
// Here the registry hands back a fresh, unauthenticated client (the
// "built too early" pod), while the shared user_secrets store already
// holds a session (the peer pod that completed the login). The status
// handler must rehydrate from the store and report Authenticated.
func TestRivianStatus_RehydratesSessionAcrossPods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	uid, err := db.EnsureUser(ctx, pool, "scott")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	store := secrets.New(pool, crypto.NoopSealer{})

	// Peer pod completed the login and persisted the session.
	want := rivian.Session{
		Email:            "driver@example.com",
		UserSessionToken: "ust-token",
		AuthenticatedAt:  time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, want); err != nil {
		t.Fatalf("SaveRivianSession: %v", err)
	}

	// This pod's registry builds a fresh, unauthenticated client —
	// exactly the "built before sign-in" state that caused the loop.
	reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient {
		return rivian.NewMock()
	})
	if reg.For(uid).Authenticated() {
		t.Fatal("precondition: fresh client must start unauthenticated")
	}

	h := handleRivianStatus(reg, store, pool, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/rivian/", nil).
		WithContext(auth.WithUser(ctx, uid))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var got rivianStatusDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Authenticated {
		t.Fatalf("Authenticated = false, want true (status poll did not rehydrate the persisted session)")
	}
	if got.Email != want.Email {
		t.Fatalf("Email = %q, want %q", got.Email, want.Email)
	}
}

// TestRivianStatus_StoredSessionWinsOverLocalPendingMFA reproduces the
// "Connected as ... then back to MFA failed" flap: pod A cached an OTP
// challenge in memory, then the user re-ran the password leg on pod B
// and Rivian no longer demanded MFA, so pod B authenticated and saved
// the session. Pod A's status poll must surrender its stale pending
// state to the stored session instead of re-offering the OTP form
// (whose dead otpToken Rivian answers with a 500).
func TestRivianStatus_StoredSessionWinsOverLocalPendingMFA(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	uid, err := db.EnsureUser(ctx, pool, "bruce")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	store := secrets.New(pool, crypto.NoopSealer{})

	// Peer pod completed the sign-in (password-only, MFA toggled off)
	// and persisted the session.
	want := rivian.Session{
		Email:            "driver@example.com",
		UserSessionToken: "ust-token",
		AuthenticatedAt:  time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, want); err != nil {
		t.Fatalf("SaveRivianSession: %v", err)
	}

	// This pod's client is stuck mid-MFA from an earlier attempt -
	// the mock arms its challenge for emails containing "mfa".
	reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient {
		m := rivian.NewMock()
		if err := m.Login(ctx, rivian.Credentials{Email: "mfa-driver@example.com", Password: "pw"}); !errors.Is(err, rivian.ErrMFARequired) {
			t.Fatalf("mock login: got %v, want ErrMFARequired", err)
		}
		return m
	})
	if !reg.For(uid).MFAPending() {
		t.Fatal("precondition: client must start MFA-pending")
	}

	h := handleRivianStatus(reg, store, pool, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/settings/rivian/", nil).
		WithContext(auth.WithUser(ctx, uid))
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	var got rivianStatusDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Authenticated {
		t.Fatal("Authenticated = false, want true (stored session must win over stale local pending MFA)")
	}
	if got.MFAPending {
		t.Fatal("MFAPending = true, want false after session restore")
	}
}

// TestClientFor_RehydratesStaleClient covers the data-plane half of
// the same multi-pod problem behind Scott's "0 samples" report: a
// request round-robined to a pod whose cached client predates the
// user's sign-in on a peer pod must not 502. clientFor loads the
// persisted session and restores the shared client before returning.
func TestClientFor_RehydratesStaleClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	uid, err := db.EnsureUser(ctx, pool, "scott")
	if err != nil {
		t.Fatalf("EnsureUser: %v", err)
	}

	store := secrets.New(pool, crypto.NoopSealer{})
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{
		Email:            "driver@example.com",
		UserSessionToken: "ust-token",
		AuthenticatedAt:  time.Unix(1_700_000_000, 0).UTC(),
	}); err != nil {
		t.Fatalf("SaveRivianSession: %v", err)
	}

	reg := rivian.NewMockAccountRegistry(func(uuid.UUID) *rivian.MockClient {
		return rivian.NewMock()
	})
	d := Deps{Accounts: reg, Secrets: store}

	if reg.For(uid).Authenticated() {
		t.Fatal("precondition: fresh client must start unauthenticated")
	}
	if c := clientFor(d, uid); c == nil {
		t.Fatal("clientFor returned nil")
	}
	// clientFor restores the shared cached instance in place, so the
	// registry's Account now reports authenticated.
	if !reg.For(uid).Authenticated() {
		t.Fatal("clientFor did not rehydrate the stale session")
	}
}
