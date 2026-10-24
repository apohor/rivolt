//go:build integration

package passkeys

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// The Postgres store: one handle per account, credentials by account and by credential id, a single-use ceremony that
// expires, and everything gone with the account.
func TestPGStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "passkeys-a")
	b, _ := db.EnsureUser(ctx, pool, "passkeys-b")
	st := NewPGStore(pool)

	h1, err := st.UserHandle(ctx, a)
	if err != nil || len(h1) != 32 {
		t.Fatalf("handle = %x, %v", h1, err)
	}
	if h2, _ := st.UserHandle(ctx, a); string(h2) != string(h1) {
		t.Error("the handle changed on a second call")
	}
	if hb, _ := st.UserHandle(ctx, b); string(hb) == string(h1) {
		t.Error("two accounts share a handle")
	}
	if uid, ok, _ := st.UserByHandle(ctx, h1); !ok || uid != a {
		t.Errorf("UserByHandle = %v, %v", uid, ok)
	}
	if _, ok, _ := st.UserByHandle(ctx, make([]byte, 32)); ok {
		t.Error("an unknown handle resolved")
	}

	cred := Credential{ID: uuid.New(), UserID: a, CredentialID: []byte("cred-a-1"), PublicKey: []byte("pk"), Transports: []string{"internal", "hybrid"},
		AAGUID: make([]byte, 16), BackupEligible: true, Label: "My phone", CreatedAt: time.Now().UTC()}
	if err := st.Add(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if err := st.Add(ctx, Credential{ID: uuid.New(), UserID: b, CredentialID: []byte("cred-a-1"), PublicKey: []byte("x"), Label: "dup", CreatedAt: time.Now()}); err == nil {
		t.Error("two accounts stored the same credential id")
	}
	got, ok, err := st.CredentialByID(ctx, []byte("cred-a-1"))
	if err != nil || !ok || got.UserID != a || got.Label != "My phone" || !got.BackupEligible || len(got.Transports) != 2 || got.LastUsedAt != nil {
		t.Fatalf("CredentialByID = %+v, %v, %v", got, ok, err)
	}
	at := time.Now().UTC().Truncate(time.Second)
	if err := st.RecordUse(ctx, []byte("cred-a-1"), 7, true, at); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.CredentialByID(ctx, []byte("cred-a-1")); got.SignCount != 7 || !got.BackupState || got.LastUsedAt == nil {
		t.Errorf("after use: %+v", got)
	}
	if list, _ := st.Credentials(ctx, a); len(list) != 1 {
		t.Errorf("a has %d passkeys", len(list))
	}
	if list, _ := st.Credentials(ctx, b); len(list) != 0 {
		t.Errorf("b sees %d passkeys", len(list))
	}
	// Only the owner renames or removes.
	if ok, _ := st.Rename(ctx, b, cred.ID, "mine now"); ok {
		t.Error("another account renamed a passkey")
	}
	if ok, _ := st.Delete(ctx, b, cred.ID); ok {
		t.Error("another account deleted a passkey")
	}
	if ok, _ := st.Rename(ctx, a, cred.ID, "Work laptop"); !ok {
		t.Error("the owner could not rename")
	}

	// A ceremony is taken once, and not after it expires.
	now := time.Now().UTC()
	c := Ceremony{ID: uuid.New(), Purpose: "login", HydraChallenge: "h", Session: []byte(`{}`), ExpiresAt: now.Add(CeremonyTTL)}
	if err := st.SaveCeremony(ctx, c); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.TakeCeremony(ctx, c.ID, "register", now); ok {
		t.Error("a login ceremony was taken as a registration")
	}
	if got, ok, _ := st.TakeCeremony(ctx, c.ID, "login", now); !ok || got.HydraChallenge != "h" || got.UserID != uuid.Nil {
		t.Errorf("take = %+v, %v", got, ok)
	}
	if _, ok, _ := st.TakeCeremony(ctx, c.ID, "login", now); ok {
		t.Error("a ceremony was taken twice")
	}
	old := Ceremony{ID: uuid.New(), Purpose: "register", UserID: a, Session: []byte(`{}`), ExpiresAt: now.Add(-time.Minute)}
	_ = st.SaveCeremony(ctx, old)
	if _, ok, _ := st.TakeCeremony(ctx, old.ID, "register", now); ok {
		t.Error("an expired ceremony was taken")
	}
	if n, _ := st.PurgeCeremonies(ctx, now); n != 1 {
		t.Errorf("purged %d, want 1", n)
	}

	// All of it goes with the account.
	if ok, _ := st.Delete(ctx, a, cred.ID); !ok {
		t.Fatal("owner could not delete")
	}
	_ = st.Add(ctx, Credential{ID: uuid.New(), UserID: a, CredentialID: []byte("cred-a-2"), PublicKey: []byte("pk"), Label: "x", CreatedAt: time.Now()})
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`SELECT count(*) FROM webauthn_credentials WHERE user_id = $1`, `SELECT count(*) FROM webauthn_users WHERE user_id = $1`} {
		var n int
		if err := pool.QueryRowContext(ctx, q, a).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s left %d rows (%v)", q, n, err)
		}
	}
}
