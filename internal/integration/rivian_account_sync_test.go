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

// A sign-in on one replica must be the sign-in on the other at once: the second pod's
// in-memory account is rebuilt from what the first stored, with the needs_reauth flag it
// cleared. This runs the real Postgres source behind rivian.AccountSyncer for two pods.
func TestRivianAccountFollowsASignInOnAnotherPod(t *testing.T) {
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
	uid, err := db.EnsureUser(ctx, pool, "two-pods")
	if err != nil {
		t.Fatal(err)
	}
	store := secrets.New(pool, crypto.NoopSealer{})
	src := secrets.NewRivianAccountSource(pool, store)

	// A pod, built the way cmd/rivolt builds one: restore the stored session and flag.
	pod := func() (rivian.AccountRegistry, *[]rivian.SyncChange) {
		reg := rivian.NewLiveAccountRegistry(func(u uuid.UUID) *rivian.LiveClient {
			lc := rivian.NewLive()
			if needs, why, err := db.GetNeedsReauth(ctx, pool, u); err == nil && needs {
				lc.SetNeedsReauth(true, why)
			}
			if sess, err := secrets.LoadRivianSession(ctx, store, u); err == nil && sess.UserSessionToken != "" {
				lc.Restore(sess)
			}
			return lc
		})
		att := reg.(interface {
			AttachSyncer(*rivian.AccountSyncer)
			Raw(uuid.UUID) rivian.Account
		})
		syn := rivian.NewAccountSyncer(src, att.Raw, nil)
		changes := &[]rivian.SyncChange{}
		syn.OnChange(func(_ uuid.UUID, c rivian.SyncChange) { *changes = append(*changes, c) })
		att.AttachSyncer(syn)
		return reg, changes
	}

	// The old session died: stored, flagged.
	oldSess := rivian.Session{Email: "owner@example.invalid", UserSessionToken: "old", AuthenticatedAt: time.Now().Add(-180 * 24 * time.Hour)}
	if err := secrets.SaveRivianSession(ctx, store, uid, oldSess); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNeedsReauth(ctx, pool, uid, "session expired"); err != nil {
		t.Fatal(err)
	}

	podA, _ := pod()
	podB, changesB := pod()
	a, b := podA.For(uid), podB.For(uid)
	for name, acct := range map[string]rivian.Account{"A": a, "B": b} {
		if needs, _ := acct.NeedsReauth(); !needs {
			t.Fatalf("pod %s should start flagged", name)
		}
	}

	// The user signs in on pod A: login stores the session and clears the flag (the sink).
	// The sync's rate limit is two seconds; wait it out as a later request would.
	newSess := rivian.Session{Email: "owner@example.invalid", UserSessionToken: "new", AuthenticatedAt: time.Now()}
	a.(*rivian.LiveClient).Restore(newSess)
	if err := secrets.SaveRivianSession(ctx, store, uid, newSess); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNeedsReauth(ctx, pool, uid, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond)

	b = podB.For(uid)
	if needs, why := b.NeedsReauth(); needs {
		t.Fatalf("pod B still needs_reauth (%q) right after the sign-in on pod A", why)
	}
	if got := b.Snapshot().UserSessionToken; got != "new" {
		t.Fatalf("pod B holds session %q, want new", got)
	}
	if len(*changesB) != 1 || (*changesB)[0] != rivian.SyncSessionReplaced {
		t.Fatalf("pod B changes = %v, want one SyncSessionReplaced", *changesB)
	}

	// Sign out on pod A: stored session deleted.
	a.Logout()
	if err := secrets.SaveRivianSession(ctx, store, uid, rivian.Session{}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond)
	if podB.For(uid).Authenticated() {
		t.Fatal("pod B still holds the session after the sign-out on pod A")
	}
	if n := len(*changesB); n != 2 || (*changesB)[1] != rivian.SyncLoggedOut {
		t.Fatalf("pod B changes = %v, want SyncSessionReplaced then SyncLoggedOut", *changesB)
	}
}
