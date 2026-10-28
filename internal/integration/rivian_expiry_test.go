//go:build integration

package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/email"
	"github.com/apohor/rivolt/internal/engage"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/rivianexpiry"
	"github.com/apohor/rivolt/internal/secrets"
	"github.com/apohor/rivolt/internal/testpg"
)

type captureMail struct{ sent []email.Message }

func (m *captureMail) Send(_ context.Context, msg email.Message) error {
	m.sent = append(m.sent, msg)
	return nil
}

// Real Postgres: the lister reads the sealed sessions, a flagged user is passed over, and
// two replicas sweeping the same table send each notice once.
func TestRivianExpiryNoticesAreSentOnceAcrossReplicas(t *testing.T) {
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
	now := time.Now()
	mk := func(name string, left time.Duration, flagged bool) uuid.UUID {
		uid, err := db.EnsureUser(ctx, pool, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE users SET email = $2, email_verified = TRUE WHERE id = $1`, uid, name+"@example.invalid"); err != nil {
			t.Fatal(err)
		}
		sess := rivian.Session{UserSessionToken: "t", AuthenticatedAt: now.Add(left - rivian.SessionLifetime)}
		if err := secrets.SaveRivianSession(ctx, store, uid, sess); err != nil {
			t.Fatal(err)
		}
		if flagged {
			if err := db.SetNeedsReauth(ctx, pool, uid, "session expired"); err != nil {
				t.Fatal(err)
			}
		}
		return uid
	}
	week := mk("exp-week", 5*24*time.Hour, false)
	mk("exp-far", 100*24*time.Hour, false)
	mk("exp-flagged", 2*24*time.Hour, true)

	mail := &captureMail{}
	replica := func() *rivianexpiry.Job {
		return &rivianexpiry.Job{
			Sessions: secrets.NewRivianSessionLister(pool, store),
			Claims:   engage.NewStore(pool),
			Mail:     mail,
			Email:    func(c context.Context, uid uuid.UUID) (string, error) { return db.LookupUserEmail(c, pool, uid) },
			BaseURL:  "https://rivolt.example",
		}
	}
	var counts rivianexpiry.Counts
	a, b := replica(), replica()
	a.OnCounts = func(c rivianexpiry.Counts) { counts = c }
	a.Sweep(ctx)
	b.Sweep(ctx)
	a.Sweep(ctx)
	if len(mail.sent) != 1 || mail.sent[0].To != "exp-week@example.invalid" {
		t.Fatalf("sent = %d messages, want exactly one, to the user 5 days from the end", len(mail.sent))
	}
	if counts.Within7 != 1 || counts.Within14 != 1 || counts.Expired != 0 {
		t.Fatalf("counts = %+v, want one session within 7 days (the flagged one is passed over)", counts)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM engagement_sends WHERE user_id = $1 AND kind = $2 AND status = 'sent' AND NOT budgeted`, week, rivianexpiry.Kind).Scan(&n); err != nil || n != 1 {
		t.Fatalf("claim rows = %d (%v), want one sent, unbudgeted", n, err)
	}
}
