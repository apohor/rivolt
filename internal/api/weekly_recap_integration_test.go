//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/statement"
	"github.com/apohor/rivolt/internal/weekly"
)

func TestWeeklyRecapUnsubscribeAndCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	mk := func(name string, verified bool) uuid.UUID {
		uid, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, verified)
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	on, off, unverified := mk("wk-on", true), mk("wk-off", true), mk("wk-unv", false)
	for u, recap := range map[uuid.UUID]bool{on: true, off: false, unverified: true} {
		if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, weekly_recap, timezone) VALUES ($1,$2,'America/Chicago')`, u, recap); err != nil {
			t.Fatal(err)
		}
	}
	src := weekly.NewSource(pool, uuid.New())
	cands, err := src.Candidates(ctx)
	if err != nil || len(cands) != 1 || cands[0].User != on || cands[0].Timezone != "America/Chicago" || cands[0].Email != "wk-on@x.test" {
		t.Fatalf("candidates %+v (%v): want only the verified, opted-in account", cands, err)
	}
	if got, _ := src.Enabled(ctx, on); !got {
		t.Error("enabled read false")
	}
	if got, _ := src.Enabled(ctx, off); got {
		t.Error("enabled read true for an account that is off")
	}

	secret := []byte("a-long-enough-secret-for-tests-1234")
	u := &weeklyUnsub{db: pool, signer: statement.NewScopedSigner(secret, "weekly"), now: time.Now}
	do := func(h http.HandlerFunc, method, tok string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, "/api/weekly-recap/unsubscribe?t="+tok, nil))
		return rec
	}
	tok := u.signer.Sign(on, "weekly:2026-W42", time.Now().Add(time.Hour))
	// GET only asks; it never unsubscribes.
	if rec := do(u.get, "GET", tok); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<form") {
		t.Errorf("get: %d %s", rec.Code, rec.Body)
	}
	if got, _ := src.Enabled(ctx, on); !got {
		t.Fatal("a GET unsubscribed the account")
	}
	// A statement token, a bad period and a tampered token do nothing.
	for name, bad := range map[string]string{
		"statement token": statement.NewSigner(secret).Sign(on, "2026-09", time.Now().Add(time.Hour)),
		"bad period":      u.signer.Sign(on, "2026-09", time.Now().Add(time.Hour)),
		"expired":         u.signer.Sign(on, "weekly:2026-W42", time.Now().Add(-time.Hour)),
		"tampered":        tok + "x",
	} {
		if rec := do(u.post, "POST", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, rec.Code)
		}
	}
	if got, _ := src.Enabled(ctx, on); !got {
		t.Fatal("a bad token unsubscribed the account")
	}
	// POST performs it, and again is the same page.
	for i := 0; i < 2; i++ {
		if rec := do(u.post, "POST", tok); rec.Code != 200 || !strings.Contains(rec.Body.String(), "unsubscribed") {
			t.Errorf("post %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	if got, _ := src.Enabled(ctx, on); got {
		t.Error("still on after the unsubscribe")
	}

	// The week's figures come from the ledger; an account with no data has an empty week.
	la := newLedgerAPI(pool, settings.NewFactory(pool))
	sum := newWeeklySummary(la, pool)
	loc, _ := time.LoadLocation("America/Chicago")
	_, s, e := weekly.Week(time.Now(), loc)
	got, err := sum(ctx, weekly.Candidate{User: off, Model: "R1S"}, s, e, loc)
	if err != nil || !got.Empty() {
		t.Errorf("summary for an empty account: %+v (%v)", got, err)
	}
}
