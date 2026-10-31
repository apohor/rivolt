//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/launchnotice"
	"github.com/apohor/rivolt/internal/testpg"
)

// The launch notice against real rows: one row per account and kind with fixed dates, an email claimed once and marked once, the API
// listing only the account's own notices within their window with the banner's dismissal remembered, and the row going with the account.
func TestLaunchNoticeAgainstPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	a, _ := db.EnsureUser(ctx, pool, "notice-a")
	b, _ := db.EnsureUser(ctx, pool, "notice-b")
	st := launchnotice.Store{DB: pool}

	now := time.Now().UTC().Truncate(24 * time.Hour)
	p := launchnotice.NewParams(now.AddDate(0, 0, 10))
	if created, err := st.Ensure(ctx, a, launchnotice.Kind, p); err != nil || !created {
		t.Fatalf("first Ensure: %v %v", created, err)
	}
	// A second Ensure, even with other dates, keeps the first row and its dates.
	if created, err := st.Ensure(ctx, a, launchnotice.Kind, launchnotice.NewParams(now.AddDate(0, 0, 40))); err != nil || created {
		t.Fatalf("second Ensure: %v %v", created, err)
	}
	states, err := st.States(ctx, launchnotice.Kind)
	if err != nil || states[a].Params != p {
		t.Fatalf("stored dates %+v (%v), want %+v", states[a].Params, err, p)
	}

	// The email: claimed once, held while the claim is fresh, retried after a release, never again once marked sent.
	got, ok, err := st.ClaimEmail(ctx, a, launchnotice.Kind)
	if err != nil || !ok || got != p {
		t.Fatalf("claim: %+v %v %v", got, ok, err)
	}
	if _, ok, _ := st.ClaimEmail(ctx, a, launchnotice.Kind); ok {
		t.Error("a second run claimed an email another run holds")
	}
	if err := st.ReleaseClaim(ctx, a, launchnotice.Kind); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ClaimEmail(ctx, a, launchnotice.Kind); !ok {
		t.Error("a released claim could not be taken again")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE account_notices SET email_claimed_at = now() - interval '1 hour' WHERE user_id = $1`, a); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ClaimEmail(ctx, a, launchnotice.Kind); !ok {
		t.Error("a stale claim (a crash between claim and send) was not reclaimable")
	}
	if err := st.MarkSent(ctx, a, launchnotice.Kind); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ClaimEmail(ctx, a, launchnotice.Kind); ok {
		t.Error("an email already sent was claimable again")
	}

	// The API.
	na := &noticesAPI{store: st, now: func() time.Time { return now }}
	list := func(uid uuid.UUID) []noticeJSON {
		rec := httptest.NewRecorder()
		na.list(uid, rec, httptest.NewRequest(http.MethodGet, "/api/notices", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		var out []noticeJSON
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if ns := list(b); len(ns) != 0 {
		t.Errorf("another account sees %v", ns)
	}
	ns := list(a)
	if len(ns) != 1 || ns[0].Kind != "launch" || ns[0].Dismissed || !strings.Contains(ns[0].Body, "no card") || len(ns[0].Links) != 2 {
		t.Fatalf("the account's notices: %+v", ns)
	}
	dismiss := func(uid uuid.UUID, body string) int {
		rec := httptest.NewRecorder()
		na.dismiss(uid, rec, httptest.NewRequest(http.MethodPost, "/api/notices/dismiss", strings.NewReader(body)))
		return rec.Code
	}
	if c := dismiss(b, `{"kind":"launch"}`); c != http.StatusNotFound {
		t.Errorf("dismissing a notice the account does not have: %d, want 404", c)
	}
	if c := dismiss(a, `{"kind":"launch"}`); c != http.StatusNoContent {
		t.Errorf("dismiss: %d", c)
	}
	if c := dismiss(a, `{"kind":"launch"}`); c != http.StatusNoContent {
		t.Errorf("dismiss again: %d, want idempotent", c)
	}
	if c := dismiss(a, `{}`); c != http.StatusBadRequest {
		t.Errorf("dismiss without a kind: %d", c)
	}
	if ns := list(a); len(ns) != 1 || !ns[0].Dismissed {
		t.Errorf("after dismissing the Settings row stays, marked dismissed: %+v", ns)
	}
	// Past its window the notice is no longer listed.
	na.now = func() time.Time { return now.AddDate(0, 0, 60) }
	if ns := list(a); len(ns) != 0 {
		t.Errorf("an expired notice is still listed: %+v", ns)
	}

	// The row goes with the account.
	if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM account_notices WHERE user_id = $1`, a).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left after the account was deleted: %d %v", n, err)
	}
}
