//go:build integration

package api

import (
	"context"
	"encoding/json"
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
