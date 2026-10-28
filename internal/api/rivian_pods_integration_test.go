//go:build integration

package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/crypto"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/secrets"
)

// rivianPod is one replica: its own registry (per-user clients built the way cmd/rivolt
// builds them, from the shared database), its own syncer and its own status handler, all
// over one Postgres. Nothing is shared between pods except the database.
type rivianPod struct {
	reg    rivian.AccountRegistry
	raw    func(uuid.UUID) rivian.Account
	syncer *rivian.AccountSyncer
	status http.HandlerFunc
}

func newRivianPod(ctx context.Context, pool *sql.DB, store *secrets.Store) *rivianPod {
	reg := rivian.NewLiveAccountRegistry(func(uid uuid.UUID) *rivian.LiveClient {
		lc := rivian.NewLive()
		lc.WithReauthSink(func(sctx context.Context, reason string) {
			if reason == "" {
				_ = db.SetNeedsReauth(sctx, pool, uid, "")
				return
			}
			_, _ = db.RaiseNeedsReauth(sctx, pool, uid, reason)
		})
		if needs, why, err := db.GetNeedsReauth(ctx, pool, uid); err == nil && needs {
			lc.SetNeedsReauth(true, why)
		}
		if sess, err := secrets.LoadRivianSession(ctx, store, uid); err == nil && sess.UserSessionToken != "" {
			lc.Restore(sess)
		}
		return lc
	})
	att := reg.(interface {
		AttachSyncer(*rivian.AccountSyncer)
		Raw(uuid.UUID) rivian.Account
	})
	syn := rivian.NewAccountSyncer(secrets.NewRivianAccountSource(pool, store), att.Raw, nil)
	att.AttachSyncer(syn)
	return &rivianPod{reg: reg, raw: att.Raw, syncer: syn, status: handleRivianStatus(reg, store, pool, nil, nil, nil)}
}

// get is GET /api/settings/rivian/ as the user, served by this pod.
func (p *rivianPod) get(ctx context.Context, t *testing.T, uid uuid.UUID) rivianStatusDTO {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/settings/rivian/", nil).WithContext(auth.WithUser(ctx, uid))
	w := httptest.NewRecorder()
	p.status(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d", w.Code)
	}
	var got rivianStatusDTO
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func (p *rivianPod) token(uid uuid.UUID) string { return p.raw(uid).Snapshot().UserSessionToken }

// With three replicas on one database, a re-link, a flag raised and a sign-out on one are
// seen by the others on their next request, for one user and not for another. This is
// the cross-pod staleness of the Oct 28 incident: a pod kept answering "needs reauth" (and
// kept the dead tokens) after the user signed in again on another pod.
func TestRivianAccountIsConsistentAcrossThreePods(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	store := secrets.New(pool, crypto.NoopSealer{})

	u1, err := db.EnsureUser(ctx, pool, "pods-one")
	if err != nil {
		t.Fatal(err)
	}
	u2, err := db.EnsureUser(ctx, pool, "pods-two")
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-180 * 24 * time.Hour)
	for _, u := range []uuid.UUID{u1, u2} {
		if err := secrets.SaveRivianSession(ctx, store, u, rivian.Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: old}); err != nil {
			t.Fatal(err)
		}
		if err := db.SetNeedsReauth(ctx, pool, u, "session expired"); err != nil {
			t.Fatal(err)
		}
	}

	a, b, c := newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store), newRivianPod(ctx, pool, store)
	pods := map[string]*rivianPod{"A": a, "B": b, "C": c}
	for name, p := range pods {
		for _, u := range []uuid.UUID{u1, u2} {
			if s := p.get(ctx, t, u); !s.NeedsReauth || !s.Authenticated {
				t.Fatalf("pod %s should start with the dead, flagged session: %+v", name, s)
			}
		}
	}

	// 1. The user re-links on pod A: A holds the new session, stores it, clears the flag.
	// (Stamped a minute back so a sign-out a moment later is not mistaken, by design, for
	// the store lagging behind a sign-in made seconds ago on that pod.)
	now := rivian.Session{Email: "d@example.invalid", UserSessionToken: "new", AuthenticatedAt: time.Now().Add(-time.Minute)}
	a.raw(u1).Restore(now)
	if err := secrets.SaveRivianSession(ctx, store, u1, now); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNeedsReauth(ctx, pool, u1, ""); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		s := pods[name].get(ctx, t, u1) // the very next request on that pod
		if s.NeedsReauth || !s.Authenticated {
			t.Fatalf("after the re-link on A, pod %s says %+v", name, s)
		}
		if got := pods[name].token(u1); got != "new" {
			t.Fatalf("after the re-link on A, pod %s holds session %q, want new", name, got)
		}
	}
	// ...and the other user is untouched on every pod.
	for name, p := range pods {
		if s := p.get(ctx, t, u2); !s.NeedsReauth {
			t.Fatalf("pod %s: the other user's flag was cleared by someone else's re-link: %+v", name, s)
		}
		if got := p.token(u2); got != "old" {
			t.Fatalf("pod %s: the other user's session changed to %q", name, got)
		}
	}

	// 2. A pod with no web traffic (the lease holder) follows through the sweep alone.
	idle := newRivianPod(ctx, pool, store)
	idle.reg.For(u2) // loaded, then the world moves on
	now2 := rivian.Session{Email: "d@example.invalid", UserSessionToken: "newer", AuthenticatedAt: time.Now()}
	if err := secrets.SaveRivianSession(ctx, store, u2, now2); err != nil {
		t.Fatal(err)
	}
	if err := db.SetNeedsReauth(ctx, pool, u2, ""); err != nil {
		t.Fatal(err)
	}
	idle.syncer.SyncAll(ctx, idle.reg.Loaded())
	if got := idle.token(u2); got != "newer" {
		t.Fatalf("the sweep did not bring the idle pod to the new session: %q", got)
	}
	if needs, _ := idle.raw(u2).NeedsReauth(); needs {
		t.Fatal("the sweep did not clear the idle pod's flag")
	}

	// 3. The reverse: a pod's client sees Rivian reject the session and raises the flag.
	if _, err := db.RaiseNeedsReauth(ctx, pool, u1, "session expired"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		if s := pods[name].get(ctx, t, u1); !s.NeedsReauth {
			t.Fatalf("pod %s did not see the flag raised elsewhere: %+v", name, s)
		}
	}
	if s := a.get(ctx, t, u2); s.NeedsReauth {
		t.Fatalf("raising one user's flag raised the other's: %+v", s)
	}

	// 4. Sign-out on pod C: the others drop the session.
	c.raw(u1).Logout()
	if err := secrets.SaveRivianSession(ctx, store, u1, rivian.Session{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		if s := pods[name].get(ctx, t, u1); s.Authenticated {
			t.Fatalf("pod %s still signed in after the sign-out on C: %+v", name, s)
		}
	}
	if got := a.token(u2); got != "newer" {
		t.Fatalf("the other user's session lost with someone else's sign-out: %q", got)
	}
}
