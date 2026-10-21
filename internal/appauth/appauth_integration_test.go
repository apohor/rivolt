//go:build integration

package appauth_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/appauth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func TestAppSignIn(t *testing.T) {
	if os.Getenv("RIVOLT_SKIP_INTEGRATION") != "" {
		t.Skip("RIVOLT_SKIP_INTEGRATION set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, err := db.EnsureUser(ctx, pool, "app-signin")
	if err != nil {
		t.Fatal(err)
	}
	store, err := appauth.New(pool, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	challenge := appauth.Challenge(verifier)

	t.Run("round trip, once", func(t *testing.T) {
		state, err := store.Begin(ctx, challenge)
		if err != nil {
			t.Fatal(err)
		}
		code, err := store.Finish(ctx, state, uid)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Finish(ctx, state, uid); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("second finish on the same state: %v", err)
		}
		got, err := store.Redeem(ctx, code, verifier)
		if err != nil || got != uid {
			t.Fatalf("redeem: %v %v", got, err)
		}
		if _, err := store.Redeem(ctx, code, verifier); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("code worked twice: %v", err)
		}
	})

	t.Run("a wrong verifier burns the code", func(t *testing.T) {
		state, _ := store.Begin(ctx, challenge)
		code, err := store.Finish(ctx, state, uid)
		if err != nil {
			t.Fatal(err)
		}
		other := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if _, err := store.Redeem(ctx, code, other); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("wrong verifier accepted: %v", err)
		}
		if _, err := store.Redeem(ctx, code, verifier); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("code survived a wrong verifier: %v", err)
		}
	})

	t.Run("an unknown state or a finish without begin", func(t *testing.T) {
		if _, err := store.Finish(ctx, "not-a-state", uid); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("finish on unknown state: %v", err)
		}
		if _, err := store.Redeem(ctx, "not-a-code", verifier); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("redeem unknown code: %v", err)
		}
		if _, err := store.Begin(ctx, "short"); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("begin with a bad challenge: %v", err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		late, err := appauth.New(pool, []byte("0123456789abcdef0123456789abcdef"))
		if err != nil {
			t.Fatal(err)
		}
		clock := time.Now()
		appauth.SetNow(late, func() time.Time { return clock })
		state, _ := late.Begin(ctx, challenge)
		clock = clock.Add(appauth.StartTTL + time.Second)
		if _, err := late.Finish(ctx, state, uid); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("finish after the start window: %v", err)
		}
		clock = time.Now()
		state, _ = late.Begin(ctx, challenge)
		code, err := late.Finish(ctx, state, uid)
		if err != nil {
			t.Fatal(err)
		}
		clock = clock.Add(appauth.CodeTTL + time.Second)
		if _, err := late.Redeem(ctx, code, verifier); !errors.Is(err, appauth.ErrInvalid) {
			t.Fatalf("redeem after the code expired: %v", err)
		}
	})

	t.Run("a cap on sign-ins in flight", func(t *testing.T) {
		capped, _ := appauth.New(pool, []byte("0123456789abcdef0123456789abcdef"))
		if _, err := pool.ExecContext(ctx, `DELETE FROM app_auth_requests`); err != nil {
			t.Fatal(err)
		}
		appauth.SetMaxOutstanding(capped, 2)
		for i := 0; i < 2; i++ {
			if _, err := capped.Begin(ctx, challenge); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := capped.Begin(ctx, challenge); !errors.Is(err, appauth.ErrBusy) {
			t.Fatalf("third start over the cap: %v", err)
		}
	})

	t.Run("purge", func(t *testing.T) {
		old, _ := appauth.New(pool, []byte("0123456789abcdef0123456789abcdef"))
		clock := time.Now().Add(-48 * time.Hour)
		appauth.SetNow(old, func() time.Time { return clock })
		if _, err := old.Begin(ctx, challenge); err != nil {
			t.Fatal(err)
		}
		clock = time.Now()
		n, err := old.Purge(ctx)
		if err != nil || n < 1 {
			t.Fatalf("purge: %d %v", n, err)
		}
	})

	t.Run("nothing raw is stored", func(t *testing.T) {
		state, _ := store.Begin(ctx, challenge)
		code, _ := store.Finish(ctx, state, uid)
		var n int
		if err := pool.QueryRowContext(ctx,
			`SELECT count(*) FROM app_auth_requests WHERE state_hash = $1::bytea OR code_hash = $2::bytea`,
			[]byte(state), []byte(code)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatal("a raw state or code is stored")
		}
	})
}
