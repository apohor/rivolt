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

// The notification endpoint's storage: a replay guard that remembers a hash for two days, and the undeliverable flag
// that the mailers and the account-delete notice honour.
func TestNotificationStorage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	st := appleid.New(pool, nil)

	h := []byte("0123456789abcdef0123456789abcdef")
	if fresh, err := st.MarkSeen(ctx, h); err != nil || !fresh {
		t.Fatalf("first sight: %v, %v", fresh, err)
	}
	if fresh, err := st.MarkSeen(ctx, h); err != nil || fresh {
		t.Fatalf("second sight: %v, %v; want a replay", fresh, err)
	}
	if err := st.Unsee(ctx, h); err != nil {
		t.Fatal(err)
	}
	if fresh, _ := st.MarkSeen(ctx, h); !fresh {
		t.Fatal("after Unsee the hash should be new again")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE apple_notification_seen SET seen_at = now() - interval '3 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeSeen(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeSeen = %d, %v; want 1", n, err)
	}
	if n, _ := st.PurgeSeen(ctx); n != 0 {
		t.Fatal("a purge of nothing")
	}

	uid, err := db.EnsureUser(ctx, pool, "apple-notify")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET email = 'n@example.com', email_verified = TRUE WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if e, err := st.DeliverableEmail(ctx, uid); err != nil || e != "n@example.com" {
		t.Fatalf("DeliverableEmail = %q, %v", e, err)
	}
	// Apple's forwarding-off event is about the relay: an account with its own address is left alone.
	if err := st.SetEmailUndeliverable(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.DeliverableEmail(ctx, uid); e != "n@example.com" {
		t.Errorf("a real address was marked undeliverable by an Apple relay event: %q", e)
	}
	// An account whose address is the relay's: marked, and cleared again.
	if _, err := pool.ExecContext(ctx, `UPDATE users SET email = 'Abc123@PrivateRelay.AppleID.com' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if err := st.SetEmailUndeliverable(ctx, uid, true); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.DeliverableEmail(ctx, uid); e != "" {
		t.Errorf("an undeliverable relay address was offered: %q", e)
	}
	if err := st.SetEmailUndeliverable(ctx, uid, false); err != nil {
		t.Fatal(err)
	}
	if e, _ := st.DeliverableEmail(ctx, uid); e != "Abc123@PrivateRelay.AppleID.com" {
		t.Errorf("cleared flag should restore the address, got %q", e)
	}
}

// A link intent: made by an authenticated start, consumed once, tied to the session that made it, replaced by a newer
// one, expired after ten minutes, dropped at sign-out and with the account.
func TestLinkIntent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, err := db.EnsureUser(ctx, pool, "link-a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.EnsureUser(ctx, pool, "link-b")
	if err != nil {
		t.Fatal(err)
	}
	st := appleid.New(pool, nil)
	now := time.Now()
	state1, state2, sessA, sessB := appleid.Hash("state-1"), appleid.Hash("state-2"), appleid.Hash("session-a"), appleid.Hash("session-b")

	if err := st.CreateLinkIntent(ctx, a, state1, sessA, now); err != nil {
		t.Fatal(err)
	}
	in, ok, err := st.ConsumeLinkIntent(ctx, state1, now.Add(time.Minute))
	if err != nil || !ok || in.UserID != a || string(in.SessionHash) != string(sessA) {
		t.Fatalf("consume = %+v, %v, %v; want a's intent", in, ok, err)
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state1, now.Add(time.Minute)); ok {
		t.Error("an intent was consumed twice")
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, appleid.Hash("never-made"), now); ok {
		t.Error("an unknown state matched an intent")
	}

	// Starting again replaces the open intent: the old state no longer works.
	if err := st.CreateLinkIntent(ctx, a, state1, sessA, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLinkIntent(ctx, a, state2, sessA, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state1, now); ok {
		t.Error("a replaced intent still worked")
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state2, now); !ok {
		t.Error("the newer intent did not work")
	}

	// Expired: refused, and the row is spent all the same.
	if err := st.CreateLinkIntent(ctx, a, state1, sessA, now); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state1, now.Add(appleid.LinkIntentTTL+time.Second)); ok {
		t.Error("an expired intent worked")
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state1, now); ok {
		t.Error("an expired intent was not spent by the refused attempt")
	}

	// Sign-out of a session drops its intents and only its.
	if err := st.CreateLinkIntent(ctx, a, state1, sessA, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateLinkIntent(ctx, b, state2, sessB, now); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteLinkIntentsForSession(ctx, sessA); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state1, now); ok {
		t.Error("a signed-out session's intent survived")
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state2, now); !ok {
		t.Error("another session's intent was dropped")
	}

	// Retention sweeps old rows; the account's deletion takes the rest.
	if err := st.CreateLinkIntent(ctx, a, state1, sessA, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE apple_link_intents SET created_at = now() - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeLinkIntents(ctx); err != nil || n != 1 {
		t.Fatalf("PurgeLinkIntents = %d, %v; want 1", n, err)
	}
	if err := st.CreateLinkIntent(ctx, b, state2, sessB, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ConsumeLinkIntent(ctx, state2, now); ok {
		t.Error("an intent outlived its account")
	}
}
