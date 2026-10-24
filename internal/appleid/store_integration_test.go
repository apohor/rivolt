//go:build integration

package appleid_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/appleid"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/testpg"
)

// The Apple identity link: one sub per account and one account per sub, the refresh token kept sealed in
// user_secrets and dropped on request, and everything gone with the account.
func TestAppleIdentityLink(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, err := db.EnsureUser(ctx, pool, "apple-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.EnsureUser(ctx, pool, "apple-b")
	if err != nil {
		t.Fatal(err)
	}
	st := appleid.New(pool, secrets.New(pool, crypto.NoopSealer{}))

	if err := st.Link(ctx, a, "sub-a", "rt-1"); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := st.Link(ctx, a, "sub-a", "rt-2"); err != nil { // the same identity again: the token is replaced
		t.Fatalf("relink: %v", err)
	}
	if tok, err := st.RefreshToken(ctx, a); err != nil || tok != "rt-2" {
		t.Errorf("token = %q, %v; want rt-2", tok, err)
	}
	if err := st.Link(ctx, b, "sub-a", "rt-x"); !errors.Is(err, appleid.ErrSubInUse) {
		t.Errorf("another account taking sub-a: %v, want ErrSubInUse", err)
	}
	if err := st.Link(ctx, a, "sub-other", ""); !errors.Is(err, appleid.ErrSubInUse) {
		t.Errorf("account a linking a second sub: %v, want ErrSubInUse", err)
	}
	if tok, _ := st.RefreshToken(ctx, b); tok != "" {
		t.Errorf("b got a token %q from a refused link", tok)
	}
	if id, ok, err := st.UserBySub(ctx, "sub-a"); err != nil || !ok || id != a {
		t.Errorf("UserBySub(sub-a) = %v, %v, %v; want a", id, ok, err)
	}
	if _, ok, err := st.UserBySub(ctx, "sub-unknown"); err != nil || ok {
		t.Errorf("UserBySub(unknown) = %v, %v; want not found", ok, err)
	}
	if u, ok, err := st.UsernameBySub(ctx, "sub-a"); err != nil || !ok || u != "apple-a" {
		t.Errorf("UsernameBySub(sub-a) = %q, %v, %v; want apple-a", u, ok, err)
	}
	if _, ok, err := st.UsernameBySub(ctx, "sub-unknown"); err != nil || ok {
		t.Errorf("UsernameBySub(unknown) = %v, %v; want not found", ok, err)
	}

	if err := st.DropToken(ctx, a); err != nil {
		t.Fatal(err)
	}
	if tok, err := st.RefreshToken(ctx, a); err != nil || tok != "" {
		t.Errorf("after DropToken: %q, %v", tok, err)
	}
	if id, ok, _ := st.UserBySub(ctx, "sub-a"); !ok || id != a {
		t.Error("DropToken removed the link; it should keep it")
	}

	if err := st.Link(ctx, a, "sub-a", "rt-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.UserBySub(ctx, "sub-a"); ok {
		t.Error("the link outlived the account")
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM user_secrets WHERE user_id = $1`, a).Scan(&n); err != nil || n != 0 {
		t.Errorf("the token outlived the account (%d rows, %v)", n, err)
	}
}
