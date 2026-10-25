//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/kratos"
	"github.com/apohor/rivolt/internal/passkeys"
	"github.com/apohor/rivolt/internal/testpg"
)

// qa's attacker story on admin-preview: support's reset said "removed 0 passkeys" while the account still held one, because the
// admin deployment runs with WebAuthn off and the deletion hung off the sign-in service. This runs the reset as the admin site is
// configured (Deps.Passkeys nil, a database, the passkey store from Deps.PasskeyStore) against real tables: the credential row must
// be gone, and the answer must say so.
func TestAdminSecureDeletesPasskeysWithWebAuthnOff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	owner, err := db.EnsureUser(ctx, pool, "secure-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET email = 'owner@x.test' WHERE id = $1`, owner); err != nil {
		t.Fatal(err)
	}
	store := passkeys.NewPGStore(pool)
	if err := store.Add(ctx, passkeys.Credential{ID: uuid.New(), UserID: owner, CredentialID: []byte("attacker-key"), PublicKey: []byte("pk"), Label: "not mine", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	k := &secKratos{hasIdent: true}
	srv := httptest.NewServer(http.HandlerFunc(k.handler))
	t.Cleanup(srv.Close)
	kc, err := kratos.New(kratos.Config{AdminURL: srv.URL, PublicURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	d := Deps{SystemDB: pool, Kratos: kc, SessionRevoker: &fakeAllSessions{}, PasskeyStore: store, Logger: discardLogger()}
	if d.Passkeys != nil {
		t.Fatal("the test must run with the sign-in service off")
	}
	sec := d.adminSecure()

	req := httptest.NewRequest(http.MethodPost, "/users/"+owner.String()+"/secure", nil)
	rc := chi.NewRouteContext()
	rc.URLParams.Add("id", owner.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	rec := httptest.NewRecorder()
	sec.handle(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"passkeys_removed":1`) {
		t.Fatalf("%d %s, want 200 and one passkey removed", rec.Code, rec.Body.String())
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, owner).Scan(&n); err != nil || n != 0 {
		t.Fatalf("credential rows left = %d (%v), want 0", n, err)
	}
}
