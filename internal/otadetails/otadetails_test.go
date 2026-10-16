package otadetails

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
	ids     []string
	due     map[string]bool // claim result per vehicle
	claimed []string
	saved   []string
}

func (f *fakeStore) Claim(_ context.Context, _ uuid.UUID, id string, _ time.Duration) (bool, error) {
	f.claimed = append(f.claimed, id)
	return f.due[id], nil
}
func (f *fakeStore) Save(_ context.Context, _ uuid.UUID, id string, _ *rivian.OTAInfo) error {
	f.saved = append(f.saved, id)
	return nil
}
func (f *fakeStore) Vehicles(context.Context, uuid.UUID) ([]string, error) { return f.ids, nil }

type fakeAcct struct {
	rivian.Account
	calls []string
	errOn string
	err   error
}

func (a *fakeAcct) OTAUpdateDetails(_ context.Context, id string) (*rivian.OTAInfo, error) {
	a.calls = append(a.calls, id)
	if id == a.errOn {
		return nil, a.err
	}
	return &rivian.OTAInfo{Current: &rivian.OTAVersion{Version: "1"}}, nil
}

type plainAcct struct{ rivian.Account }

type registry struct{ a rivian.Account }

func (r registry) For(uuid.UUID) rivian.Account { return r.a }
func (r registry) Loaded() []uuid.UUID          { return nil }

func newTest(st *fakeStore, a rivian.Account) (*Syncer, *[]time.Duration) {
	s := newSyncer(st, registry{a}, nil, nil)
	var sleeps []time.Duration
	s.sleep = func(_ context.Context, d time.Duration) { sleeps = append(sleeps, d) }
	return s, &sleeps
}

// Only vehicles whose claim is won are fetched; a vehicle fetched in the
// last 23 hours (claim lost) costs no Rivian call, and fetches are spaced.
func TestSyncUserFetchesOnlyDueVehicles(t *testing.T) {
	st := &fakeStore{ids: []string{"a", "b", "c"}, due: map[string]bool{"a": true, "b": false, "c": true}}
	acct := &fakeAcct{}
	s, sleeps := newTest(st, acct)
	n, err := s.SyncUser(context.Background(), uuid.New())
	if err != nil || n != 2 {
		t.Fatalf("fetched %d, err %v; want 2", n, err)
	}
	if strings.Join(acct.calls, ",") != "a,c" || strings.Join(st.saved, ",") != "a,c" {
		t.Errorf("calls %v saved %v", acct.calls, st.saved)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != vehiclePace {
		t.Errorf("sleeps = %v, want one %v between the two fetches", *sleeps, vehiclePace)
	}
	// A second pass with nothing due makes no call at all.
	st.due = map[string]bool{}
	acct.calls = nil
	if n, _ := s.SyncUser(context.Background(), uuid.New()); n != 0 || len(acct.calls) != 0 {
		t.Errorf("nothing due but fetched %d (%v)", n, acct.calls)
	}
}

func TestSyncUserStopsAtFirstUpstreamErrorAndSkipsOtherAccounts(t *testing.T) {
	st := &fakeStore{ids: []string{"a", "b"}, due: map[string]bool{"a": true, "b": true}}
	acct := &fakeAcct{errOn: "a", err: errors.New("upstream paused")}
	s, _ := newTest(st, acct)
	if _, err := s.SyncUser(context.Background(), uuid.New()); err == nil || strings.Join(acct.calls, ",") != "a" || len(st.saved) != 0 {
		t.Errorf("err %v calls %v saved %v", err, acct.calls, st.saved)
	}
	// A replay/mock account is not an OTASource: nothing is claimed.
	st2 := &fakeStore{ids: []string{"a"}, due: map[string]bool{"a": true}}
	s2, _ := newTest(st2, &plainAcct{})
	if n, err := s2.SyncUser(context.Background(), uuid.New()); err != nil || n != 0 || len(st2.claimed) != 0 {
		t.Errorf("non-OTA account: n %d err %v claimed %v", n, err, st2.claimed)
	}
}

func TestDescribeKeepsResponseBodiesOut(t *testing.T) {
	err := fmt.Errorf("getOTAUpdateDetails: %w", &rivian.UpstreamError{Class: rivian.ClassTransient, Op: "getOTAUpdateDetails", HTTPStatus: 502, Reason: "bad gateway", Cause: errors.New(`HTTP 502: {"version":"2026.38.1","url":"https://x"}`)})
	got := describe(err)
	if strings.Contains(got, "2026.38.1") || strings.Contains(got, "https://x") || !strings.Contains(got, "http=502") {
		t.Errorf("describe = %s", got)
	}
}
