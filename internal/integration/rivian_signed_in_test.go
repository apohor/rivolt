//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/testpg"
)

// countingSealer counts the sessions opened.
type countingSealer struct {
	crypto.Sealer
	opens atomic.Int64
}

func (c *countingSealer) Open(ctx context.Context, uid uuid.UUID, blob []byte) ([]byte, error) {
	c.opens.Add(1)
	return c.Sealer.Open(ctx, uid, blob)
}

func signedInAt(ctx context.Context, t *testing.T, pool *sql.DB, uid uuid.UUID) sql.NullTime {
	t.Helper()
	var at sql.NullTime
	if err := pool.QueryRowContext(ctx, `SELECT rivian_signed_in_at FROM users WHERE id = $1`, uid).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// The sign-in time is plain metadata beside the session (M4 precondition of the single-owner
// design): written with the session at sign-in, cleared at sign-out, and the expiry sweep reads
// only it: a session is opened once to backfill a time that was never recorded, never again.
func TestRivianSignInTimeIsRecordedAndTheSweepNeverOpensSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	sealer := &countingSealer{Sealer: crypto.NoopSealer{}}
	store := secrets.New(pool, sealer)
	mk := func(name string) uuid.UUID {
		uid, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	signedIn := time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Second)

	// Sign-in writes the time with the session; sign-out clears it.
	a := mk("si-a")
	if err := secrets.SaveRivianSession(ctx, store, a, rivian.Session{UserSessionToken: "t", AuthenticatedAt: signedIn}); err != nil {
		t.Fatal(err)
	}
	if at := signedInAt(ctx, t, pool, a); !at.Valid || !at.Time.Equal(signedIn) {
		t.Fatalf("after sign-in: %+v, want %v", at, signedIn)
	}
	if err := secrets.SaveRivianSession(ctx, store, a, rivian.Session{}); err != nil {
		t.Fatal(err)
	}
	if at := signedInAt(ctx, t, pool, a); at.Valid {
		t.Fatalf("sign-out left %v", at.Time)
	}
	// A session with no sign-in time records none.
	if err := secrets.SaveRivianSession(ctx, store, a, rivian.Session{UserSessionToken: "t"}); err != nil {
		t.Fatal(err)
	}
	if at := signedInAt(ctx, t, pool, a); at.Valid {
		t.Fatalf("a session without a time recorded %v", at.Time)
	}

	// Sessions written before the column existed: the first pass opens each once to backfill.
	old := mk("si-old")
	oldAt := time.Now().Add(-100 * 24 * time.Hour).Truncate(time.Second)
	if err := store.Put(ctx, old, "rivian.session", []byte(`{"user_session_token":"t","authenticated_at":"`+oldAt.UTC().Format(time.RFC3339)+`"}`)); err != nil {
		t.Fatal(err)
	}
	var logBuf bytes.Buffer
	lister := secrets.NewRivianSessionLister(pool, store).WithLog(slog.New(slog.NewTextHandler(&logBuf, nil)))
	got := entriesByUser(ctx, t, lister)
	if !got[old].SignedIn.Equal(oldAt) {
		t.Fatalf("backfilled time = %v, want %v", got[old].SignedIn, oldAt)
	}
	if at := signedInAt(ctx, t, pool, old); !at.Valid || !at.Time.Equal(oldAt) {
		t.Fatalf("backfill did not record the time: %+v", at)
	}
	if !got[a].SignedIn.IsZero() {
		t.Errorf("a session with no sign-in time reported %v", got[a].SignedIn)
	}

	if !strings.Contains(logBuf.String(), "missing=2") {
		t.Errorf("the first pass should log the two sessions it had to backfill:\n%s", logBuf.String())
	}
	logBuf.Reset()

	// From then on a pass opens nothing, and the times are the same.
	before := sealer.opens.Load()
	again := entriesByUser(ctx, t, lister)
	if n := sealer.opens.Load() - before; n != 0 {
		t.Errorf("a pass opened %d sessions after the backfill", n)
	}
	if !strings.Contains(logBuf.String(), "missing=0") {
		t.Errorf("a pass after the backfill should log missing=0:\n%s", logBuf.String())
	}
	if !again[old].SignedIn.Equal(oldAt) || !again[a].SignedIn.IsZero() {
		t.Errorf("times changed: %+v", again)
	}

	// A sign-in between the read and the backfill wins: the backfill only fills a NULL.
	raced := mk("si-race")
	if err := store.Put(ctx, raced, "rivian.session", []byte(`{"user_session_token":"t","authenticated_at":"2020-01-01T00:00:00Z"}`)); err != nil {
		t.Fatal(err)
	}
	fresh := time.Now().Truncate(time.Second)
	if _, err := pool.ExecContext(ctx, `UPDATE users SET rivian_signed_in_at = $2 WHERE id = $1`, raced, fresh); err != nil {
		t.Fatal(err)
	}
	if got := entriesByUser(ctx, t, lister)[raced].SignedIn; !got.Equal(fresh) {
		t.Errorf("a recorded sign-in was overwritten by the backfill: %v", got)
	}
}

func entriesByUser(ctx context.Context, t *testing.T, l *secrets.RivianSessionLister) map[uuid.UUID]struct{ SignedIn time.Time } {
	t.Helper()
	es, err := l.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out := map[uuid.UUID]struct{ SignedIn time.Time }{}
	for _, e := range es {
		out[e.User] = struct{ SignedIn time.Time }{e.SignedIn}
	}
	return out
}
