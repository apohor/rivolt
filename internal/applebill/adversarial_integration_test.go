//go:build integration

package applebill

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/testpg"
)

// Pro 30, database half: hostile but validly signed input against real rows. The unit half (adversarial_test.go) is everything refused before the
// database; here the question is what a genuine-looking value does to the entitlement: expired and revoked reports, a replay raced against itself,
// events older than or equal to what was applied, and a notification carrying another account's token.

type advEnv struct {
	tb          testing.TB
	ctx         context.Context
	pool        *sql.DB
	chain       *testChain
	notif       *Handler
	txn         *Transaction
	mu          sync.Mutex
	outcomes    []string
	invalidated []uuid.UUID
}

func newAdvEnv(t *testing.T) *advEnv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	pool, err := db.Open(ctx, testpg.DSN(ctx, t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	e := &advEnv{tb: t, ctx: ctx, pool: pool, chain: newChain(t, chainOpts{})}
	inv := func(u uuid.UUID) { e.mu.Lock(); e.invalidated = append(e.invalidated, u); e.mu.Unlock() }
	obs := func(o string) { e.mu.Lock(); e.outcomes = append(e.outcomes, o); e.mu.Unlock() }
	e.notif = &Handler{DB: pool, Verifier: e.chain.verifier, Config: testConfig(), Invalidate: inv, Observe: obs}
	e.txn = &Transaction{DB: pool, Verifier: e.chain.verifier, Config: testConfig(), Invalidate: inv}
	return e
}

func (e *advEnv) user(t *testing.T) uuid.UUID {
	t.Helper()
	u, err := db.EnsureUser(e.ctx, e.pool, "adv-"+uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *advEnv) report(uid uuid.UUID, tx TransactionInfo) (int, string) {
	b, _ := json.Marshal(map[string]string{"signedTransaction": e.chain.sign(e.tb, tx)})
	rec := httptest.NewRecorder()
	e.txn.Serve(uid, rec, httptest.NewRequest(http.MethodPost, "/api/billing/apple/transaction", strings.NewReader(string(b))))
	return rec.Code, rec.Body.String()
}

func (e *advEnv) entitlement(t *testing.T, u uuid.UUID) (status string, end time.Time, cancel bool, ok bool) {
	t.Helper()
	var n sql.NullTime
	err := e.pool.QueryRowContext(e.ctx, `SELECT status, current_period_end, cancel_at_period_end FROM entitlements WHERE user_id = $1 AND source = 'apple'`, u).Scan(&status, &n, &cancel)
	if err == sql.ErrNoRows {
		return "", time.Time{}, false, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return status, n.Time.UTC(), cancel, true
}

func (e *advEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRowContext(e.ctx, q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (e *advEnv) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	o := e.outcomes
	e.outcomes = nil
	return o
}

func (e *advEnv) takeInvalidated() []uuid.UUID {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.invalidated
	e.invalidated = nil
	return i
}

func (e *advEnv) send(typ, sub, id string, at time.Time, tx TransactionInfo) (int, string) {
	e.take()
	code := post(e.notif, notification(e.chain, e.tb, typ, sub, id, at, tx)).Code
	o := e.take()
	if len(o) == 0 {
		return code, ""
	}
	return code, o[len(o)-1]
}

// A report that is expired or revoked grants nothing, a refund then a report cannot revive Pro, and a report for an account that does not exist
// links nothing.
func TestAdversarialReportsAgainstPostgres(t *testing.T) {
	e := newAdvEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	live := now.Add(30 * 24 * time.Hour)
	rid := func() string { return "rv-test-adv-" + uuid.NewString()[:8] }

	t.Run("expired transaction", func(t *testing.T) {
		u := e.user(t)
		orig := rid()
		code, body := e.report(u, ti(orig, now.Add(-time.Hour), u.String()))
		if strings.Contains(body, `"pro":true`) {
			t.Fatalf("an expired transaction granted Pro: %d %s", code, body)
		}
		if st, end, _, ok := e.entitlement(t, u); ok && st == "active" && end.After(now) {
			t.Errorf("an expired transaction left an active entitlement: %s %v", st, end)
		}
	})

	t.Run("revoked transaction", func(t *testing.T) {
		u := e.user(t)
		tx := ti(rid(), live, u.String())
		tx.RevocationDate = now.UnixMilli()
		code, body := e.report(u, tx)
		if strings.Contains(body, `"pro":true`) {
			t.Fatalf("a revoked transaction granted Pro: %d %s", code, body)
		}
		if st, _, _, ok := e.entitlement(t, u); ok && st == "active" {
			t.Errorf("a revoked transaction left an active entitlement")
		}
	})

	t.Run("a report cannot revive a refunded subscription", func(t *testing.T) {
		u := e.user(t)
		tx := ti(rid(), live, u.String())
		tx.SignedDate = now.Add(-time.Hour).UnixMilli()
		if code, body := e.report(u, tx); code != http.StatusOK || !strings.Contains(body, `"pro":true`) {
			t.Fatalf("the first report: %d %s", code, body)
		}
		refund := tx
		refund.RevocationDate = now.UnixMilli()
		if code, o := e.send("REFUND", "", uuid.NewString(), now, refund); code != http.StatusOK || o != OutcomeApplied {
			t.Fatalf("refund: %d %s", code, o)
		}
		// the original (pre-refund) JWS, replayed
		if _, body := e.report(u, tx); strings.Contains(body, `"pro":true`) {
			t.Errorf("replaying the pre-refund transaction revived Pro: %s", body)
		}
		if st, _, _, _ := e.entitlement(t, u); st == "active" {
			t.Errorf("entitlement after the replay: %s", st)
		}
	})

	t.Run("an account that does not exist", func(t *testing.T) {
		ghost := uuid.New()
		orig := rid()
		code, _ := e.report(ghost, ti(orig, live, ghost.String()))
		if code == http.StatusOK {
			t.Errorf("a report for a missing account: %d, want a refusal", code)
		}
		if n := e.count(t, `SELECT count(*) FROM billing_apple_subs WHERE original_transaction_id = $1`, orig); n != 0 {
			t.Errorf("a link row exists for a missing account: %d", n)
		}
	})

	t.Run("the same report raced against itself", func(t *testing.T) {
		u := e.user(t)
		tx := ti(rid(), live, u.String())
		var wg sync.WaitGroup
		codes := make([]int, 12)
		for i := range codes {
			wg.Add(1)
			go func() { defer wg.Done(); codes[i], _ = e.report(u, tx) }()
		}
		wg.Wait()
		for i, c := range codes {
			if c != http.StatusOK {
				t.Errorf("racer %d: %d, want 200", i, c)
			}
		}
		if n := e.count(t, `SELECT count(*) FROM billing_apple_subs WHERE original_transaction_id = $1`, tx.OriginalTransactionID); n != 1 {
			t.Errorf("link rows: %d, want 1", n)
		}
		if n := e.count(t, `SELECT count(*) FROM entitlements WHERE user_id = $1 AND source = 'apple'`, u); n != 1 {
			t.Errorf("entitlement rows: %d, want 1", n)
		}
	})
}

// A notification replayed concurrently applies once; events older than what was applied change nothing; an equal signedDate with the same
// content is idempotent; a transaction carrying another account's token moves only the account the subscription is linked to.
func TestAdversarialNotificationsAgainstPostgres(t *testing.T) {
	e := newAdvEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	end1, end2 := now.Add(30*24*time.Hour), now.Add(60*24*time.Hour)

	link := func(t *testing.T) (uuid.UUID, TransactionInfo) {
		t.Helper()
		u := e.user(t)
		tx := ti("rv-test-adv-"+uuid.NewString()[:8], end1, u.String())
		tx.SignedDate = now.Add(-time.Hour).UnixMilli()
		if code, body := e.report(u, tx); code != http.StatusOK || !strings.Contains(body, `"pro":true`) {
			t.Fatalf("link: %d %s", code, body)
		}
		return u, tx
	}

	t.Run("concurrent replay of one notification uuid", func(t *testing.T) {
		u, tx := link(t)
		e.takeInvalidated()
		renew := tx
		renew.ExpiresDate = end2.UnixMilli()
		id := uuid.NewString()
		body := notification(e.chain, t, "DID_RENEW", "", id, now, renew)
		e.take()
		var wg sync.WaitGroup
		codes := make([]int, 16)
		for i := range codes {
			wg.Add(1)
			go func() { defer wg.Done(); codes[i] = post(e.notif, body).Code }()
		}
		wg.Wait()
		applied, dup := 0, 0
		for _, o := range e.take() {
			switch o {
			case OutcomeApplied:
				applied++
			case OutcomeDuplicate:
				dup++
			}
		}
		for i, c := range codes {
			if c != http.StatusOK {
				t.Errorf("racer %d: %d, want 200", i, c)
			}
		}
		if applied != 1 || dup != len(codes)-1 {
			t.Errorf("applied %d, duplicate %d; want 1 and %d", applied, dup, len(codes)-1)
		}
		if n := e.count(t, `SELECT count(*) FROM apple_events WHERE notification_uuid = $1`, id); n != 1 {
			t.Errorf("event rows: %d, want 1", n)
		}
		if got := e.takeInvalidated(); len(got) != 1 || got[0] != u {
			t.Errorf("plan cache drops: %v, want exactly one for the account", got)
		}
		if _, end, _, _ := e.entitlement(t, u); !end.Equal(end2) {
			t.Errorf("period end %v, want %v", end, end2)
		}
	})

	t.Run("older events change nothing, whatever their type", func(t *testing.T) {
		u, tx := link(t)
		renew := tx
		renew.ExpiresDate = end2.UnixMilli()
		if code, o := e.send("DID_RENEW", "", uuid.NewString(), now, renew); code != http.StatusOK || o != OutcomeApplied {
			t.Fatalf("renew: %d %s", code, o)
		}
		wantSt, wantEnd, wantCancel, _ := e.entitlement(t, u)
		e.takeInvalidated()
		refund := tx
		refund.RevocationDate = now.Add(-time.Minute).UnixMilli()
		for _, c := range []struct {
			typ, sub string
			tx       TransactionInfo
		}{
			{"DID_RENEW", "", tx},
			{"DID_FAIL_TO_RENEW", "GRACE_PERIOD", tx},
			{"GRACE_PERIOD_EXPIRED", "", tx},
			{"EXPIRED", "VOLUNTARY", tx},
			{"DID_CHANGE_RENEWAL_STATUS", "AUTO_RENEW_DISABLED", tx},
			{"REFUND", "", refund},
			{"REVOKE", "", refund},
		} {
			code, o := e.send(c.typ, c.sub, uuid.NewString(), now.Add(-time.Minute), c.tx)
			if code != http.StatusOK || (o != OutcomeStale && o != OutcomeIgnored) {
				t.Errorf("%s older than the renewal: %d %s, want stale or ignored", c.typ, code, o)
			}
			if st, end, cancel, _ := e.entitlement(t, u); st != wantSt || !end.Equal(wantEnd) || cancel != wantCancel {
				t.Fatalf("%s older than the renewal moved the entitlement: %s %v %v, want %s %v %v", c.typ, st, end, cancel, wantSt, wantEnd, wantCancel)
			}
		}
		if got := e.takeInvalidated(); len(got) != 0 {
			t.Errorf("stale events dropped the plan cache: %v", got)
		}
	})

	t.Run("an equal signedDate with the same content is idempotent", func(t *testing.T) {
		u, tx := link(t)
		renew := tx
		renew.ExpiresDate = end2.UnixMilli()
		if code, o := e.send("DID_RENEW", "", uuid.NewString(), now, renew); code != http.StatusOK || o != OutcomeApplied {
			t.Fatalf("renew: %d %s", code, o)
		}
		st, end, cancel, _ := e.entitlement(t, u)
		if code, _ := e.send("DID_RENEW", "", uuid.NewString(), now, renew); code != http.StatusOK {
			t.Fatalf("equal-date renew: %d", code)
		}
		if st2, end2b, cancel2, _ := e.entitlement(t, u); st2 != st || !end2b.Equal(end) || cancel2 != cancel {
			t.Errorf("an equal-date repeat changed the entitlement: %s %v %v -> %s %v %v", st, end, cancel, st2, end2b, cancel2)
		}
	})

	t.Run("another account's token inside a notification moves only the linked account", func(t *testing.T) {
		alice, tx := link(t)
		bob := e.user(t)
		forged := tx
		forged.AppAccountToken = bob.String()
		forged.ExpiresDate = end2.UnixMilli()
		if code, o := e.send("DID_RENEW", "", uuid.NewString(), now, forged); code != http.StatusOK || o != OutcomeApplied {
			t.Fatalf("forged-token renewal: %d %s", code, o)
		}
		if _, _, _, ok := e.entitlement(t, bob); ok {
			t.Error("a notification naming bob's token created an entitlement for bob")
		}
		if _, end, _, _ := e.entitlement(t, alice); !end.Equal(end2) {
			t.Errorf("the linked account's period end %v, want %v", end, end2)
		}
	})
}
