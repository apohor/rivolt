//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/testpg"
)

type openCounter struct {
	crypto.Sealer
	opens int
}

func (o *openCounter) Open(ctx context.Context, uid uuid.UUID, blob []byte) ([]byte, error) {
	o.opens++
	return o.Sealer.Open(ctx, uid, blob)
}

// Real Postgres: HasRivianSession is the stored row, answered without opening it, and a sign-out takes it back to false. A pod that does not own
// the account uses it at sign-in so it decrypts nothing.
func TestHasRivianSessionReadsTheRowWithoutDecrypting(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	sealer := &openCounter{Sealer: crypto.NoopSealer{}}
	store := secrets.New(pool, sealer)
	uid, err := db.EnsureUser(ctx, pool, "has-session")
	if err != nil {
		t.Fatal(err)
	}
	has := func() bool {
		ok, err := secrets.HasRivianSession(ctx, store, uid)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if has() {
		t.Fatal("no row yet: want false")
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{UserSessionToken: "t", AuthenticatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !has() {
		t.Fatal("stored session: want true")
	}
	if sealer.opens != 0 {
		t.Fatalf("HasRivianSession opened the sealed row %d times, want 0", sealer.opens)
	}
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{}); err != nil {
		t.Fatal(err)
	}
	if has() {
		t.Fatal("after sign-out: want false")
	}
}
