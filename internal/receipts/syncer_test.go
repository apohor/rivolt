package receipts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/rivian"
)

type fakeStore struct {
	claimOK     bool
	claims      []string
	results     []error
	upserted    []rivian.SessionSummary
	unmatched   []Receipt
	charges     []ChargeRef
	applied     []Pair
	refreshed   int
	needDetail  []string
	saved       []string
	savedCharge []*rivian.SessionCharger
}

func (f *fakeStore) ClaimSummaries(context.Context, uuid.UUID, time.Duration) (bool, error) {
	f.claims = append(f.claims, "summaries")
	return f.claimOK, nil
}
func (f *fakeStore) ClaimDetails(context.Context, uuid.UUID, time.Duration) (bool, error) {
	f.claims = append(f.claims, "details")
	return f.claimOK, nil
}
func (f *fakeStore) MarkResult(_ context.Context, _ uuid.UUID, err error) error {
	f.results = append(f.results, err)
	return nil
}
func (f *fakeStore) UpsertReceipts(_ context.Context, _ uuid.UUID, in []rivian.SessionSummary) error {
	f.upserted = append(f.upserted, in...)
	return nil
}
func (f *fakeStore) Unmatched(context.Context, uuid.UUID) ([]Receipt, error) { return f.unmatched, nil }
func (f *fakeStore) CandidateCharges(context.Context, uuid.UUID, time.Time, time.Time) ([]ChargeRef, error) {
	return f.charges, nil
}
func (f *fakeStore) Apply(_ context.Context, _ uuid.UUID, p []Pair) error {
	f.applied = append(f.applied, p...)
	return nil
}
func (f *fakeStore) RefreshMatched(context.Context, uuid.UUID) error { f.refreshed++; return nil }
func (f *fakeStore) NeedDetail(_ context.Context, _ uuid.UUID, limit int) ([]string, error) {
	if len(f.needDetail) > limit {
		return f.needDetail[:limit], nil
	}
	return f.needDetail, nil
}
func (f *fakeStore) SaveDetail(_ context.Context, _ uuid.UUID, txn string, c *rivian.SessionCharger) error {
	f.saved = append(f.saved, txn)
	f.savedCharge = append(f.savedCharge, c)
	return nil
}

type fakeAcct struct {
	rivian.Account
	sums        []rivian.SessionSummary
	sumsErr     error
	sumCalls    int
	detailCalls []string
	detailErr   error
	// onFetch runs after each Rivian call returns (a user opting out mid-pass).
	onFetch func()
}

func (a *fakeAcct) CompletedSessionSummaries(context.Context) ([]rivian.SessionSummary, error) {
	a.sumCalls++
	if a.onFetch != nil {
		defer a.onFetch()
	}
	return a.sums, a.sumsErr
}
func (a *fakeAcct) SessionCharger(_ context.Context, txn string) (*rivian.SessionCharger, error) {
	a.detailCalls = append(a.detailCalls, txn)
	if a.onFetch != nil {
		defer a.onFetch()
	}
	if a.detailErr != nil {
		return nil, a.detailErr
	}
	home := false
	return &rivian.SessionCharger{Name: "Station", Vendor: "ChargePoint", IsHome: &home}, nil
}

type fakeRegistry struct{ a rivian.Account }

func (r fakeRegistry) For(uuid.UUID) rivian.Account { return r.a }
func (r fakeRegistry) Loaded() []uuid.UUID          { return nil }

func newTestSyncer(st *fakeStore, a *fakeAcct, on bool) (*Syncer, *[]time.Duration) {
	s := newSyncer(st, fakeRegistry{a}, func(uuid.UUID) bool { return on }, nil, nil)
	var sleeps []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) { sleeps = append(sleeps, d) }
	return s, &sleeps
}

func TestSyncFetchesMatchesAndPacesDetails(t *testing.T) {
	st := &fakeStore{claimOK: true}
	c := chg(0, 60, 40)
	r := rcpt(1, 59, 40)
	st.charges, st.unmatched = []ChargeRef{c}, []Receipt{r}
	for i := 0; i < 14; i++ {
		st.needDetail = append(st.needDetail, "txn")
	}
	a := &fakeAcct{sums: []rivian.SessionSummary{{TransactionID: "t1"}}}
	s, sleeps := newTestSyncer(st, a, true)

	res, err := s.Sync(context.Background(), uuid.New(), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.sumCalls != 1 || len(st.upserted) != 1 || len(st.applied) != 1 || st.refreshed != 1 {
		t.Errorf("summaries %d upserted %d applied %d refreshed %d", a.sumCalls, len(st.upserted), len(st.applied), st.refreshed)
	}
	if res.Matched != 1 || res.Fetched != 1 {
		t.Errorf("result = %+v", res)
	}
	// 14 waiting, capped at maxDetailPerRun, spaced by detailPace, rest left.
	if len(a.detailCalls) != maxDetailPerRun || !res.DetailsLeft {
		t.Errorf("detail calls %d, left %v; want %d and true", len(a.detailCalls), res.DetailsLeft, maxDetailPerRun)
	}
	if len(*sleeps) != maxDetailPerRun-1 {
		t.Fatalf("%d sleeps, want %d", len(*sleeps), maxDetailPerRun-1)
	}
	for _, d := range *sleeps {
		if d != detailPace {
			t.Errorf("sleep %v, want %v", d, detailPace)
		}
	}
	if len(st.results) != 1 || st.results[0] != nil {
		t.Errorf("results = %v", st.results)
	}
}

func TestSyncSkipsWhenClaimedOrDisabled(t *testing.T) {
	a := &fakeAcct{}
	st := &fakeStore{claimOK: false}
	s, _ := newTestSyncer(st, a, true)
	res, err := s.Sync(context.Background(), uuid.New(), time.Hour, false)
	if err != nil || !res.Skipped || a.sumCalls != 0 {
		t.Errorf("claimed elsewhere: res %+v err %v calls %d", res, err, a.sumCalls)
	}
	st = &fakeStore{claimOK: true}
	s, _ = newTestSyncer(st, a, false)
	res, err = s.Sync(context.Background(), uuid.New(), time.Hour, false)
	if err != nil || !res.Skipped || len(st.claims) != 0 || a.sumCalls != 0 {
		t.Errorf("flag off: res %+v claims %v calls %d", res, st.claims, a.sumCalls)
	}
}

func TestSyncDetailsOnlyNeverFetchesSummaries(t *testing.T) {
	st := &fakeStore{claimOK: true, needDetail: []string{"a", "b"}}
	a := &fakeAcct{}
	s, _ := newTestSyncer(st, a, true)
	if _, err := s.Sync(context.Background(), uuid.New(), time.Hour, true); err != nil {
		t.Fatal(err)
	}
	if a.sumCalls != 0 || len(a.detailCalls) != 2 || strings.Join(st.claims, ",") != "details" {
		t.Errorf("summaries %d details %v claims %v", a.sumCalls, a.detailCalls, st.claims)
	}
}

// A failing detail call (breaker open, kill switch, rate limit) stops the
// pass and is recorded; nothing after it is attempted.
func TestSyncStopsAtFirstUpstreamError(t *testing.T) {
	st := &fakeStore{claimOK: true, needDetail: []string{"a", "b", "c"}}
	a := &fakeAcct{detailErr: errors.New("upstream paused")}
	s, _ := newTestSyncer(st, a, true)
	res, err := s.Sync(context.Background(), uuid.New(), time.Hour, true)
	if err == nil || len(a.detailCalls) != 1 || !res.DetailsLeft {
		t.Errorf("err %v calls %v left %v", err, a.detailCalls, res.DetailsLeft)
	}
	if len(st.results) != 1 || st.results[0] == nil {
		t.Errorf("failure not recorded: %v", st.results)
	}
	a2 := &fakeAcct{sumsErr: errors.New("upstream paused")}
	st2 := &fakeStore{claimOK: true}
	s2, _ := newTestSyncer(st2, a2, true)
	if _, err := s2.Sync(context.Background(), uuid.New(), time.Hour, false); err == nil || len(st2.upserted) != 0 {
		t.Errorf("summaries error: err %v upserted %d", err, len(st2.upserted))
	}
}

type fakeTimer struct{ f func() }

func (fakeTimer) Stop() bool { return true }

// Many closes for one user while a timer is pending schedule one sync, and
// a user with the flag off schedules none.
func TestOnChargeCloseDebouncesPerUser(t *testing.T) {
	st := &fakeStore{claimOK: true}
	a := &fakeAcct{}
	s, _ := newTestSyncer(st, a, true)
	var scheduled []fakeTimer
	var delays []time.Duration
	s.after = func(d time.Duration, f func()) stopper {
		delays = append(delays, d)
		scheduled = append(scheduled, fakeTimer{f})
		return scheduled[len(scheduled)-1]
	}
	u := uuid.New()
	s.OnChargeClose(u)
	s.OnChargeClose(u)
	s.OnChargeClose(u)
	if len(scheduled) != 1 || delays[0] != closeDelay {
		t.Fatalf("scheduled %d timers, delays %v", len(scheduled), delays)
	}
	scheduled[0].f()
	if a.sumCalls != 1 {
		t.Errorf("the timer ran %d syncs, want 1", a.sumCalls)
	}
	s.OnChargeClose(u) // pending was cleared, so a later close schedules again
	if len(scheduled) != 2 {
		t.Errorf("a close after the timer fired did not schedule again")
	}
	off, _ := newTestSyncer(st, a, false)
	off.after = func(time.Duration, func()) stopper {
		t.Error("scheduled for a user with the flag off")
		return fakeTimer{}
	}
	off.OnChargeClose(u)
}

// The privacy line: the detail document selects the charger's name, vendor
// and isHomeCharger and nothing else. No payment method or card, no order,
// no address.
func TestDetailQueryAsksForNothingPrivate(t *testing.T) {
	q := rivian.QSessionChargerForTest()
	for _, banned := range []string{"paymentMethod", "card", "brand", "lastFour", "order", "paidTotal", "address", "line1", "postalCode", "city"} {
		if strings.Contains(q, banned) {
			t.Errorf("detail query selects %q:\n%s", banned, q)
		}
	}
	for _, want := range []string{"name", "vendor", "isHomeCharger"} {
		if !strings.Contains(q, want) {
			t.Errorf("detail query lacks %q", want)
		}
	}
}

// A response body fragment in an upstream error's cause must not reach the
// stored last_error or a log line.
func TestDescribeKeepsResponseBodiesOut(t *testing.T) {
	cause := errors.New(`HTTP 500: {"totalCost":12.34,"city":"Reno","card":"4242"}`)
	err := fmt.Errorf("GetCompletedSessionSummaries: %w", &rivian.UpstreamError{Class: rivian.ClassTransient, Op: "GetCompletedSessionSummaries", HTTPStatus: 500, Reason: "server error", Cause: cause})
	got := describe(err)
	for _, leak := range []string{"12.34", "Reno", "4242", "totalCost"} {
		if strings.Contains(got, leak) {
			t.Errorf("describe leaks %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "GetCompletedSessionSummaries") || !strings.Contains(got, "http=500") {
		t.Errorf("describe lost the classification: %s", got)
	}
	if d := describe(errors.New("pq: duplicate key value violates unique constraint (secret)")); strings.Contains(d, "secret") {
		t.Errorf("unknown errors must not echo their text: %s", d)
	}

	// And the syncer stores the sanitized form.
	st := &fakeStore{claimOK: true}
	a := &fakeAcct{sumsErr: err}
	s, _ := newTestSyncer(st, a, true)
	_, _ = s.Sync(context.Background(), uuid.New(), time.Hour, false)
	if len(st.results) != 1 || st.results[0] == nil || strings.Contains(st.results[0].Error(), "Reno") {
		t.Errorf("stored result = %v", st.results)
	}
}

// A user who opts out while a pass is running (opting out deletes their
// receipts) gets nothing saved from the Rivian response that arrives after.
func TestSyncStopsSavingWhenTheUserOptsOutMidPass(t *testing.T) {
	for _, tc := range []struct {
		name        string
		detailsOnly bool
	}{{"summaries", false}, {"details", true}} {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{claimOK: true, needDetail: []string{"t1", "t2"}}
			on := true
			a := &fakeAcct{sums: []rivian.SessionSummary{{TransactionID: "t1"}}, onFetch: func() { on = false }}
			s := newSyncer(st, fakeRegistry{a}, func(uuid.UUID) bool { return on }, nil, nil)
			s.sleep = func(context.Context, time.Duration) {}
			if _, err := s.Sync(context.Background(), uuid.New(), time.Hour, tc.detailsOnly); err != nil {
				t.Fatal(err)
			}
			if len(st.upserted) != 0 || len(st.applied) != 0 || len(st.saved) != 0 {
				t.Errorf("saved after opt-out: upserted %d applied %d details %d", len(st.upserted), len(st.applied), len(st.saved))
			}
		})
	}
}
