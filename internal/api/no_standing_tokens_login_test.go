//go:build integration

package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/metrics"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

type countingSealer struct {
	crypto.Sealer
	opens atomic.Int32
}

func (c *countingSealer) Open(ctx context.Context, uid uuid.UUID, blob []byte) ([]byte, error) {
	c.opens.Add(1)
	return c.Sealer.Open(ctx, uid, blob)
}

// Real Postgres, the live registry behind the M4-5 gate and a stub Rivian: a sign-in on a pod that does not hold the account stores the
// session, decrypts nothing and leaves no token loaded; the pod that holds it then adopts the stored session by decrypting it once.
func TestSignInOnAPodThatDoesNotHoldTheAccountDecryptsNothing(t *testing.T) {
	ctx, pool := openFunnelDB(t)
	uid, err := db.EnsureUser(ctx, pool, "non-owner-signin")
	if err != nil {
		t.Fatal(err)
	}
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case bytes.Contains(raw, [](byte)("CreateCSRFToken")):
			_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"c","appSessionToken":"a"}}}`)
		case bytes.Contains(raw, [](byte)("\"Login\"")):
			_, _ = io.WriteString(w, `{"data":{"login":{"__typename":"MobileLoginResponse","userSessionToken":"u"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	sealer := &countingSealer{Sealer: crypto.NoopSealer{}}
	store := secrets.New(pool, sealer)
	build := func(uuid.UUID) *rivian.LiveClient { return rivian.NewLive().WithEndpoint(stub.URL + "/gateway") }
	reg := rivian.NewLiveAccountRegistry(build)
	var held atomic.Bool
	reg.(interface {
		SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool), func(uuid.UUID) *rivian.LiveClient)
	}).SetNoStandingTokens(func(context.Context, uuid.UUID) (bool, bool) { return held.Load(), true }, build)
	unheld := func() int {
		return reg.(interface {
			UnheldTokens(func(uuid.UUID) bool) int
		}).UnheldTokens(func(uuid.UUID) bool { return held.Load() })
	}

	login := handleRivianLogin(reg, store, nil, nil, pool, metrics.New(), nil)
	code, body := postJSON(t, ctx, login, uid, rivianLoginReq{Email: "driver@example.invalid", Password: "pw"})
	if code != http.StatusOK || body["authenticated"] != true {
		t.Fatalf("sign-in on the non-owner = %d %v, want 200 authenticated", code, body)
	}
	if n := sealer.opens.Load(); n != 0 {
		t.Errorf("non-owner sign-in decrypted %d times, want 0", n)
	}
	if ok, err := secrets.HasRivianSession(ctx, store, uid); err != nil || !ok {
		t.Fatalf("session not stored: %v %v", ok, err)
	}
	if n := unheld(); n != 0 {
		t.Errorf("unheld tokens after the sign-in = %d, want 0", n)
	}
	if reg.For(uid) != nil {
		t.Error("the non-owner holds a client for the account")
	}
	if n := sealer.opens.Load(); n != 0 {
		t.Errorf("non-owner reads decrypted %d times, want 0", n)
	}

	// The owner adopts the stored session.
	held.Store(true)
	sess, err := secrets.LoadRivianSession(ctx, store, uid)
	if err != nil || sess.UserSessionToken != "u" {
		t.Fatalf("owner load: %v %q", err, sess.UserSessionToken)
	}
	if n := sealer.opens.Load(); n != 1 {
		t.Errorf("owner decrypts = %d, want 1", n)
	}
}
