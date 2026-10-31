//go:build integration

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/auth"
	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/r2arrival"
	"github.com/apohor/rivolt/internal/statement"
)

// The R2 arrival month: set, changed and deleted through the engagement settings (a month, never a day, null deletes, absent keeps),
// shown only to its owner's mail job while the address is verified and no truck is connected, deleted by the email's signed link, and
// gone with the account.
func TestR2ArrivalMonth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	uid, _ := db.EnsureUserVerified(ctx, pool, "r2a", "r2a@x.test", "r2a", true)
	unverified, _ := db.EnsureUserVerified(ctx, pool, "r2a-unv", "r2a-unv@x.test", "r2a-unv", false)
	a := &engagementAPI{db: pool}
	put := func(u uuid.UUID, body string) (int, engagementSettings) {
		req := httptest.NewRequest("PUT", "/api/settings/engagement", strings.NewReader(body)).WithContext(auth.WithUser(ctx, u))
		rec := httptest.NewRecorder()
		a.put(u, rec, req)
		var out engagementSettings
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 2, 0)
	m := month.Format("2006-01")

	if _, s := put(uid, `{"weekly_recap":true}`); s.R2ArrivalMonth != nil {
		t.Fatalf("off until chosen: %v", *s.R2ArrivalMonth)
	}
	if code, s := put(uid, `{"r2_arrival_month":"`+m+`"}`); code != 200 || s.R2ArrivalMonth == nil || *s.R2ArrivalMonth != m || !s.WeeklyRecap {
		t.Fatalf("set: %d %+v", code, s)
	}
	if code, s := put(uid, `{"trip_recap":true}`); code != 200 || s.R2ArrivalMonth == nil || *s.R2ArrivalMonth != m {
		t.Fatalf("absent keeps the month: %d %+v", code, s)
	}
	for _, bad := range []string{`"2026-12-15"`, `"December"`, `"1999-01"`, `"` + month.AddDate(0, 30, 0).Format("2006-01") + `"`, `5`, `""`} {
		if code, _ := put(uid, `{"r2_arrival_month":`+bad+`}`); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, code)
		}
	}
	var day int
	if err := pool.QueryRowContext(ctx, `SELECT EXTRACT(day FROM r2_arrival_month)::int FROM engagement_prefs WHERE user_id = $1`, uid).Scan(&day); err != nil || day != 1 {
		t.Fatalf("stored as the first of the month: %d %v", day, err)
	}

	// Due: the owner of a verified address with no truck; not an unverified address; not once a vehicle is connected.
	put(unverified, `{"r2_arrival_month":"`+m+`"}`)
	ids := func() map[uuid.UUID]bool {
		due, err := db.ListR2ArrivalDue(ctx, pool, month, month)
		if err != nil {
			t.Fatal(err)
		}
		out := map[uuid.UUID]bool{}
		for _, d := range due {
			out[d.UserID] = true
			if d.Month.Day() != 1 {
				t.Errorf("month %v", d.Month)
			}
		}
		return out
	}
	if got := ids(); !got[uid] || got[unverified] {
		t.Fatalf("due: %v (verified %v, unverified %v)", got, uid, unverified)
	}
	if ok, err := db.R2ArrivalStillWanted(ctx, pool, uid, month); err != nil || !ok {
		t.Fatalf("still wanted: %v %v", ok, err)
	}
	if ok, _ := db.R2ArrivalStillWanted(ctx, pool, uid, month.AddDate(0, 1, 0)); ok {
		t.Error("a different month is not the one asked for")
	}
	if _, err := pool.ExecContext(ctx, `UPDATE users SET engagement_emails_off_at = now() WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if got := ids(); got[uid] {
		t.Error("engagement emails off: not due")
	}
	pool.ExecContext(ctx, `UPDATE users SET engagement_emails_off_at = NULL WHERE id = $1`, uid)
	if _, err := pool.ExecContext(ctx, `INSERT INTO vehicles (user_id, rivian_vehicle_id, model) VALUES ($1, 'r2a-rv', 'R2')`, uid); err != nil {
		t.Fatal(err)
	}
	if got := ids(); got[uid] {
		t.Error("a vehicle is connected: not due")
	}
	pool.ExecContext(ctx, `DELETE FROM vehicles WHERE user_id = $1`, uid)

	// The email's link deletes it, only with a token signed for this purpose.
	signer := statement.NewSigner([]byte("0123456789abcdef0123456789abcdef"))
	link := &r2ArrivalEmailAPI{db: pool, signer: signer, now: time.Now}
	call := func(h http.HandlerFunc, method, token string) int {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(method, "/api/r2-arrival/unsubscribe?t="+token, nil))
		return rec.Code
	}
	wrong := signer.Sign(uid, "onboarding", time.Now().Add(time.Hour))
	if code := call(link.unsubscribePost, "POST", wrong); code != http.StatusBadRequest {
		t.Errorf("a token for another purpose: %d", code)
	}
	tok := signer.Sign(uid, r2arrival.UnsubPurpose, time.Now().Add(time.Hour))
	if code := call(link.unsubscribeGet, "GET", tok); code != 200 {
		t.Errorf("GET: %d", code)
	}
	if got := ids(); !got[uid] {
		t.Error("a GET (a link scanner) must not delete the reminder")
	}
	if code := call(link.unsubscribePost, "POST", tok); code != 200 {
		t.Errorf("POST: %d", code)
	}
	if got := ids(); got[uid] {
		t.Error("deleted by the link")
	}
	if _, s := put(uid, `{"r2_arrival_month":"`+m+`"}`); s.R2ArrivalMonth == nil {
		t.Fatal("set again")
	}
	if _, s := put(uid, `{"r2_arrival_month":null}`); s.R2ArrivalMonth != nil {
		t.Errorf("null deletes it: %v", *s.R2ArrivalMonth)
	}

	// Deleting the account takes the row with it.
	put(uid, `{"r2_arrival_month":"`+m+`"}`)
	if err := db.DeleteUserAccount(ctx, pool, uid); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM engagement_prefs WHERE user_id = $1`, uid).Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left after account delete: %d %v", n, err)
	}
}

// The due list is capped at 500, so users whose email is done must leave it: 501 finished rows must not hide the 502nd, and a month
// that has ended is deleted by the sweep's one statement, not kept.
func TestR2ArrivalFinishedRowsDoNotCrowdOutLaterUsers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, crossTenantDSN(ctx, t))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	now := time.Now().UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var ids []uuid.UUID
	for i := 0; i < 502; i++ {
		name := "r2a-cap-" + strconv.Itoa(i)
		u, err := db.EnsureUserVerified(ctx, pool, name, name+"@x.test", name, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, r2_arrival_month) VALUES ($1, $2::date)
			ON CONFLICT (user_id) DO UPDATE SET r2_arrival_month = EXCLUDED.r2_arrival_month`, u, month.Format("2006-01-02")); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u)
	}
	last := ids[len(ids)-1]
	// Order is by month then id: find the user the cap would hide, whichever it is.
	due, err := db.ListR2ArrivalDue(ctx, pool, month, month)
	if err != nil || len(due) != 500 {
		t.Fatalf("the list is capped at 500: %d %v", len(due), err)
	}
	listed := map[uuid.UUID]bool{}
	for _, d := range due {
		listed[d.UserID] = true
	}
	var hidden []uuid.UUID
	for _, u := range ids {
		if !listed[u] {
			hidden = append(hidden, u)
		}
	}
	if len(hidden) != 2 {
		t.Fatalf("two users beyond the cap: %d", len(hidden))
	}
	// Their emails go: the job deletes each listed user's month; then the hidden ones are listed.
	for _, d := range due {
		if err := db.ClearR2ArrivalMonthFor(ctx, pool, d.UserID, d.Month); err != nil {
			t.Fatal(err)
		}
	}
	due, err = db.ListR2ArrivalDue(ctx, pool, month, month)
	if err != nil || len(due) != 2 {
		t.Fatalf("after the first 500 are done the rest are listed: %d %v", len(due), err)
	}
	// A changed month is not deleted by a stale clear.
	if err := db.ClearR2ArrivalMonthFor(ctx, pool, last, month.AddDate(0, 3, 0)); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM engagement_prefs WHERE user_id = $1 AND r2_arrival_month IS NOT NULL`, last).Scan(&n)
	if n != 1 {
		t.Errorf("a clear for another month deleted this one")
	}

	// Ended months: two months back and earlier go; last month (a zone behind UTC may still be in it) and this one stay.
	for off, name := range map[int]string{-3: "old", -2: "older", -1: "prev"} {
		u, _ := db.EnsureUserVerified(ctx, pool, "r2a-end-"+name, "r2a-end-"+name+"@x.test", name, true)
		pool.ExecContext(ctx, `INSERT INTO engagement_prefs (user_id, r2_arrival_month) VALUES ($1, $2::date)
			ON CONFLICT (user_id) DO UPDATE SET r2_arrival_month = EXCLUDED.r2_arrival_month`, u, month.AddDate(0, off, 0).Format("2006-01-02"))
	}
	if _, err := db.ClearEndedR2ArrivalMonths(ctx, pool, now); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM engagement_prefs p JOIN users u ON u.id = p.user_id
		WHERE u.username LIKE 'r2a-end-%' AND p.r2_arrival_month IS NOT NULL`).Scan(&left)
	if left != 1 {
		t.Errorf("only last month's row stays: %d", left)
	}
}
