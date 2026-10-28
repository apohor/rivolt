//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/testpg"
)

const legacyBlob = `{"email":"a@example.invalid","csrf_token":"c","app_session_token":"a","user_session_token":"u",` +
	`"access_token":"legacy-access","refresh_token":"legacy-refresh","authenticated_at":"2026-05-01T00:47:00Z"}`

// A session sealed by an older version holds an access and a refresh token nothing ever used.
// It still loads; the hourly pass removes the two fields without changing the row's version
// (so no pod sees a new session), and never overwrites a sign-in that landed in between.
func TestLegacyRivianTokensAreScrubbedWithoutChangingTheVersion(t *testing.T) {
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
	store := secrets.New(pool, crypto.NoopSealer{})
	uid, err := db.EnsureUser(ctx, pool, "scrub-one")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, uid, "rivian.session", []byte(legacyBlob)); err != nil {
		t.Fatal(err)
	}

	// The old blob still loads, with no trace of the legacy fields in the Go value.
	sess, err := secrets.LoadRivianSession(ctx, store, uid)
	if err != nil || sess.UserSessionToken != "u" || sess.Email != "a@example.invalid" || sess.AuthenticatedAt.IsZero() {
		t.Fatalf("old blob: %+v %v", sess, err)
	}
	_, v1, err := store.GetVersioned(ctx, uid, "rivian.session")
	if err != nil {
		t.Fatal(err)
	}

	ok, err := secrets.ScrubLegacyRivianTokens(ctx, store, uid)
	if err != nil || !ok {
		t.Fatalf("scrub: %v %v, want it to remove the legacy fields", ok, err)
	}
	raw, v2, err := store.GetVersioned(ctx, uid, "rivian.session")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "legacy-") || strings.Contains(string(raw), "access_token") || strings.Contains(string(raw), "refresh_token") {
		t.Fatalf("the stored session still holds a legacy field: %s", raw)
	}
	if !v2.Equal(v1) {
		t.Fatalf("scrubbing changed the row's version (%v -> %v): every pod would think it is a new session", v1, v2)
	}
	after, _ := secrets.LoadRivianSession(ctx, store, uid)
	if after != sess {
		t.Fatalf("the session changed: %+v -> %+v", sess, after)
	}
	// Nothing left to do: a second pass writes nothing.
	if ok, err := secrets.ScrubLegacyRivianTokens(ctx, store, uid); err != nil || ok {
		t.Fatalf("second scrub: %v %v, want nothing to do", ok, err)
	}

	// A sign-in that lands between the read and the replace is not overwritten.
	uid2, err := db.EnsureUser(ctx, pool, "scrub-two")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, uid2, "rivian.session", []byte(legacyBlob)); err != nil {
		t.Fatal(err)
	}
	_, readAt, err := store.GetVersioned(ctx, uid2, "rivian.session")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	fresh := rivian.Session{Email: "a@example.invalid", UserSessionToken: "new-login", AuthenticatedAt: time.Now()}
	if err := secrets.SaveRivianSession(ctx, store, uid2, fresh); err != nil { // the user signs in again on another pod
		t.Fatal(err)
	}
	replaced, err := store.ReplaceIfUnchanged(ctx, uid2, "rivian.session", []byte(`{"user_session_token":"stale"}`), readAt)
	if err != nil || replaced {
		t.Fatalf("ReplaceIfUnchanged over a newer write: %v %v, want false", replaced, err)
	}
	got, _ := secrets.LoadRivianSession(ctx, store, uid2)
	if got.UserSessionToken != "new-login" {
		t.Fatalf("the newer sign-in was overwritten: %+v", got)
	}

	// The lister logs one line per pass with counts only: here the second user's session was
	// replaced by a clean one and the first is already clean, so it scrubbed nothing; a third
	// legacy session is scrubbed by the pass and counted.
	uidLegacy, _ := db.EnsureUser(ctx, pool, "scrub-four")
	if err := store.Put(ctx, uidLegacy, "rivian.session", []byte(legacyBlob)); err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	lister := secrets.NewRivianSessionLister(pool, store).WithLog(slog.New(slog.NewTextHandler(&logBuf, nil)))
	if _, err := lister.List(ctx); err != nil {
		t.Fatal(err)
	}
	if line := logBuf.String(); !strings.Contains(line, "rivian session scrub") || !strings.Contains(line, "scrubbed=1") || strings.Contains(line, "example.invalid") || strings.Contains(line, "legacy-") {
		t.Fatalf("scrub log line = %q, want counts only: scrubbed=1", line)
	}
	logBuf.Reset()
	if _, err := lister.List(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logBuf.String(), "scrubbed=0") {
		t.Fatalf("second pass line = %q, want scrubbed=0", logBuf.String())
	}

	// A session with nothing legacy in it, and a user with none, are left alone.
	uid3, _ := db.EnsureUser(ctx, pool, "scrub-three")
	if ok, err := secrets.ScrubLegacyRivianTokens(ctx, store, uid3); err != nil || ok {
		t.Fatalf("no session: %v %v", ok, err)
	}
	if ok, err := secrets.ScrubLegacyRivianTokens(ctx, store, uid2); err != nil || ok {
		t.Fatalf("a clean session: %v %v", ok, err)
	}
}
