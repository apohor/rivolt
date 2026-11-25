//go:build integration

package applebill

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/entitlements"
	"github.com/apohor/rivolt/internal/testpg"
)

// App Review buys in the Sandbox against production. With the flag on, only the allowlisted account's Sandbox purchase counts; it is capped at
// a day, tagged, never counted, never over a real paid row; anyone else's (a TestFlight tester's) is refused with a message the app can show;
// and with the flag off at start, the row ends.
func TestAppleSandboxReview(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	reviewer, _ := db.EnsureUser(ctx, pool, "sbx-reviewer")
	tester, _ := db.EnsureUser(ctx, pool, "sbx-tester")
	payer, _ := db.EnsureUser(ctx, pool, "sbx-payer")
	if _, err := pool.ExecContext(ctx, `INSERT INTO apple_sandbox_accounts (user_id) VALUES ($1)`, reviewer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO apple_sandbox_accounts (user_id) VALUES ($1)`, payer); err != nil { // allowlisted AND really paying
		t.Fatal(err)
	}
	chain := newChain(t, chainOpts{})
	cfg := testConfig()
	cfg.AcceptSandbox = true
	refused := 0
	var outcomes []string
	notif := &Handler{DB: pool, Verifier: chain.verifier, Config: cfg, Observe: func(o string) { outcomes = append(outcomes, o) }}
	txn := &Transaction{DB: pool, Verifier: chain.verifier, Config: cfg, SandboxRefused: func() { refused++ }}
	report := func(u uuid.UUID, x TransactionInfo) (int, string) {
		b, _ := json.Marshal(map[string]string{"signedTransaction": chain.sign(t, x)})
		rec := httptest.NewRecorder()
		txn.Serve(u, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(string(b))))
		return rec.Code, rec.Body.String()
	}
	row := func(u uuid.UUID) (status string, end time.Time, sandbox, ok bool) {
		err := pool.QueryRowContext(ctx, `SELECT status, current_period_end, sandbox FROM entitlements WHERE user_id = $1 AND source = 'apple'`, u).Scan(&status, &end, &sandbox)
		if err == sql.ErrNoRows {
			return "", time.Time{}, false, false
		}
		if err != nil {
			t.Fatal(err)
		}
		return status, end, sandbox, true
	}
	now := time.Now().UTC()
	sbx := func(orig string, u uuid.UUID) TransactionInfo {
		x := ti(orig, now.Add(30*24*time.Hour), u.String()) // Apple's sandbox says 30 days; the cap must win
		x.Environment = EnvSandbox
		return x
	}

	// A tester off the allowlist: 403, a clear code, nothing written, counted.
	if code, body := report(tester, sbx("sbx-orig-tester", tester)); code != http.StatusForbidden || !strings.Contains(body, "sandbox_not_allowed") {
		t.Fatalf("tester: %d %s", code, body)
	}
	if _, _, _, ok := row(tester); ok || refused != 1 {
		t.Errorf("tester row written or refusal not counted (%v, %d)", ok, refused)
	}
	var links int
	_ = pool.QueryRowContext(ctx, `SELECT count(*) FROM billing_apple_subs WHERE original_transaction_id = 'sbx-orig-tester'`).Scan(&links)
	if links != 0 {
		t.Error("a refused Sandbox purchase left a link")
	}

	// The review account: Pro, tagged, capped at a day.
	if code, body := report(reviewer, sbx("sbx-orig-reviewer", reviewer)); code != http.StatusOK || !strings.Contains(body, `"pro":true`) {
		t.Fatalf("reviewer: %d %s", code, body)
	}
	st, end, sandbox, _ := row(reviewer)
	if st != "active" || !sandbox || end.After(time.Now().Add(SandboxCap+time.Minute)) {
		t.Errorf("reviewer row: %s %v sandbox=%v", st, end, sandbox)
	}

	// A notification for it applies; for an unlinked or off-allowlist Sandbox subscription it is dropped (200, not retried).
	renew := sbx("sbx-orig-reviewer", reviewer)
	renew.SignedDate = time.Now().UnixMilli()
	outcomes = nil
	if rec := post(notif, notification(chain, t, "DID_RENEW", "", uuid.NewString(), time.Now(), renew)); rec.Code != http.StatusOK || outcomes[len(outcomes)-1] != OutcomeApplied {
		t.Errorf("reviewer renew: %d %v", rec.Code, outcomes)
	}
	if _, end, _, _ := row(reviewer); end.After(time.Now().Add(SandboxCap + time.Minute)) {
		t.Errorf("a renewal lifted the cap: %v", end)
	}
	outcomes = nil
	if rec := post(notif, notification(chain, t, "SUBSCRIBED", "INITIAL_BUY", uuid.NewString(), time.Now(), sbx("sbx-orig-nobody", tester))); rec.Code != http.StatusOK || outcomes[len(outcomes)-1] != OutcomeIgnored {
		t.Errorf("unlinked sandbox notification: %d %v", rec.Code, outcomes)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM apple_sandbox_accounts WHERE user_id = $1`, reviewer); err != nil {
		t.Fatal(err)
	}
	outcomes = nil
	renew.SignedDate = time.Now().UnixMilli() + 1000
	if rec := post(notif, notification(chain, t, "DID_RENEW", "", uuid.NewString(), time.Now().Add(time.Second), renew)); rec.Code != http.StatusOK || outcomes[len(outcomes)-1] != OutcomeIgnored {
		t.Errorf("notification after the allowlist entry was removed: %d %v", rec.Code, outcomes)
	}

	// A real paid Apple row is never replaced or extended by a Sandbox purchase on the same account.
	realEnd := now.Add(10 * 24 * time.Hour)
	if _, err := pool.ExecContext(ctx, `INSERT INTO entitlements (user_id, source, status, current_period_end, external_id) VALUES ($1, 'apple', 'active', $2, 'real-orig')`, payer, realEnd); err != nil {
		t.Fatal(err)
	}
	report(payer, sbx("sbx-orig-payer", payer))
	if st, end, sandbox, _ := row(payer); sandbox || st != "active" || !end.Equal(realEnd.Truncate(time.Microsecond)) && end.Sub(realEnd).Abs() > time.Second {
		t.Errorf("a Sandbox purchase touched a real row: %s %v sandbox=%v", st, end, sandbox)
	}

	// The counts leave Sandbox rows out.
	if _, err := pool.ExecContext(ctx, `UPDATE entitlements SET status = 'active' WHERE user_id = $1`, reviewer); err != nil {
		t.Fatal(err)
	}
	before, err := entitlements.ReadStats(ctx, pool, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE entitlements SET sandbox = FALSE WHERE user_id = $1`, reviewer); err != nil {
		t.Fatal(err)
	}
	after, _ := entitlements.ReadStats(ctx, pool, uuid.New())
	k := [2]string{"apple", "active"}
	if after.ByKind[k] != before.ByKind[k]+1 {
		t.Errorf("Sandbox row counted: %d vs %d (untagged)", before.ByKind[k], after.ByKind[k])
	}
	if _, err := pool.ExecContext(ctx, `UPDATE entitlements SET sandbox = TRUE WHERE user_id = $1`, reviewer); err != nil {
		t.Fatal(err)
	}

	// Flag off at start: every Sandbox row ends; the real one stays.
	n, err := EndSandbox(ctx, pool)
	if err != nil || n < 1 {
		t.Fatalf("EndSandbox: %d %v", n, err)
	}
	if st, _, _, _ := row(reviewer); st != "ended" {
		t.Errorf("reviewer row after the flag went off: %s", st)
	}
	if st, _, _, _ := row(payer); st != "active" {
		t.Errorf("the real row ended with the Sandbox ones: %s", st)
	}

	// Flag off: a Sandbox transaction is simply rejected like any other environment.
	off := &Transaction{DB: pool, Verifier: chain.verifier, Config: testConfig()}
	rec := httptest.NewRecorder()
	b, _ := json.Marshal(map[string]string{"signedTransaction": chain.sign(t, sbx("sbx-orig-off", reviewer))})
	off.Serve(reviewer, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(string(b))))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("flag off, Sandbox transaction: %d", rec.Code)
	}
}
