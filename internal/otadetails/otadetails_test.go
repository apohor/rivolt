package otadetails

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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
	// unavailable records MarkUnavailable calls.
	unavailable []string
	notices     map[string]bool
}

func (f *fakeStore) Claim(_ context.Context, _ uuid.UUID, id string, _ time.Duration) (bool, error) {
	f.claimed = append(f.claimed, id)
	return f.due[id], nil
}
func (f *fakeStore) Save(_ context.Context, _ uuid.UUID, id string, _ *rivian.OTAInfo) error {
	f.saved = append(f.saved, id)
	return nil
}
func (f *fakeStore) MarkUnavailable(_ context.Context, _ uuid.UUID, id string) error {
	f.unavailable = append(f.unavailable, id)
	return nil
}
func (f *fakeStore) Vehicles(context.Context, uuid.UUID) ([]string, error) { return f.ids, nil }

// Notice bookkeeping, like the Postgres side: the first Seed per vehicle
// wins, ClaimNotice is insert-if-absent.
func (f *fakeStore) Seed(_ context.Context, _ uuid.UUID, id, installed, available string) (bool, error) {
	if f.notices == nil {
		f.notices = map[string]bool{}
	}
	for k := range f.notices {
		if strings.HasPrefix(k, id+"|") {
			return false, nil
		}
	}
	seeded := false
	for kind, v := range map[string]string{"installed": installed, "available": available} {
		if v != "" {
			f.notices[id+"|"+kind+"|"+v] = true
			seeded = true
		}
	}
	return seeded, nil
}
func (f *fakeStore) ClaimNotice(_ context.Context, _ uuid.UUID, id, kind, v string) (bool, error) {
	k := id + "|" + kind + "|" + v
	if f.notices[k] {
		return false, nil
	}
	f.notices[k] = true
	return true, nil
}
func (f *fakeStore) Label(context.Context, uuid.UUID, string) string { return "R1S" }

type fakeAcct struct {
	rivian.Account
	calls []string
	errOn string
	err   error
	// background is whether the last fetch's context carried the background budget class.
	background bool
}

func (a *fakeAcct) OTAUpdateDetails(ctx context.Context, id string) (*rivian.OTAInfo, error) {
	a.calls = append(a.calls, id)
	a.background = rivian.IsBackground(ctx)
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
	if !acct.background {
		t.Error("the OTA fetch did not carry the background budget class")
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

// A FORBIDDEN, unsupported or GraphQL-level refusal for one vehicle (some R2s)
// is expected: it is marked unavailable (a week's back-off in the claim), the
// pass goes on with the user's other vehicles and returns no error. A
// transport failure or an outage still stops the pass for a retry.
func TestSyncUserBacksOffNotAvailableAndContinues(t *testing.T) {
	forbidden := &rivian.UpstreamError{Class: rivian.ClassUserAction, Op: "getOTAUpdateDetails", ExtCode: "FORBIDDEN", Cause: errors.New("User is forbidden from accessing this resource")}
	graphql := &rivian.UpstreamError{Class: rivian.ClassUnknown, Op: "getOTAUpdateDetails", Cause: errors.New("some graphql message")}
	for name, e := range map[string]error{"forbidden": forbidden, "graphql-level refusal": graphql} {
		st := &fakeStore{ids: []string{"r2", "r1"}, due: map[string]bool{"r2": true, "r1": true}}
		acct := &fakeAcct{errOn: "r2", err: e}
		s, _ := newTest(st, acct)
		n, err := s.SyncUser(context.Background(), uuid.New())
		if err != nil || n != 1 || strings.Join(st.unavailable, ",") != "r2" || strings.Join(st.saved, ",") != "r1" {
			t.Errorf("%s: fetched %d err %v unavailable %v saved %v", name, n, err, st.unavailable, st.saved)
		}
	}
	for name, e := range map[string]error{
		"outage":    &rivian.UpstreamError{Class: rivian.ClassOutage, Op: "getOTAUpdateDetails", HTTPStatus: 502},
		"transport": &rivian.UpstreamError{Class: rivian.ClassUnknown, Op: "getOTAUpdateDetails", Cause: &url.Error{Op: "Post", URL: "x", Err: errors.New("tls: handshake failure")}},
	} {
		st := &fakeStore{ids: []string{"a", "b"}, due: map[string]bool{"a": true, "b": true}}
		acct := &fakeAcct{errOn: "a", err: e}
		s, _ := newTest(st, acct)
		if _, err := s.SyncUser(context.Background(), uuid.New()); err == nil || len(st.unavailable) != 0 || len(acct.calls) != 1 {
			t.Errorf("%s: err %v unavailable %v calls %v; want a stop, no back-off", name, err, st.unavailable, acct.calls)
		}
	}
}

func info(cur, avail string) *rivian.OTAInfo {
	i := &rivian.OTAInfo{Current: &rivian.OTAVersion{Version: cur}}
	if avail != "" {
		i.Available = &rivian.OTAVersion{Version: avail, ReleaseNotesURL: "https://rivian.example/notes/" + avail}
	}
	return i
}

// The first sync of a vehicle is a silent baseline; a new available version
// alerts once; the same version again (a restart, the next day's sync) does
// not; installing it alerts once as "installed".
func TestAnnounceBaselineThenNewVersionThenInstalledOnce(t *testing.T) {
	st := &fakeStore{notices: map[string]bool{}}
	s, _ := newTest(st, &fakeAcct{})
	var got []Update
	s.SetNotifier(func(u Update) { got = append(got, u) })
	uid := uuid.New()
	ctx := context.Background()

	s.announce(ctx, uid, "v1", info("2026.30", "2026.36")) // baseline: silent
	if len(got) != 0 {
		t.Fatalf("first sync alerted: %v", got)
	}
	s.announce(ctx, uid, "v1", info("2026.30", "2026.36")) // same again
	if len(got) != 0 {
		t.Fatalf("known version alerted: %v", got)
	}
	s.announce(ctx, uid, "v1", info("2026.30", "2026.38")) // new offer
	s.announce(ctx, uid, "v1", info("2026.30", "2026.38")) // restart: no repeat
	if len(got) != 1 || got[0].Installed || got[0].Version != "2026.38" || got[0].Vehicle != "R1S" {
		t.Fatalf("available alert = %+v", got)
	}
	s.announce(ctx, uid, "v1", info("2026.38", "")) // installed
	s.announce(ctx, uid, "v1", info("2026.38", ""))
	if len(got) != 2 || !got[1].Installed || got[1].Version != "2026.38" {
		t.Fatalf("installed alert = %+v", got)
	}
}

func TestAnnounceWithoutNotifierOrInfoIsNoOp(t *testing.T) {
	st := &fakeStore{notices: map[string]bool{}}
	s, _ := newTest(st, &fakeAcct{})
	s.announce(context.Background(), uuid.New(), "v1", info("1", "2")) // no notifier
	s.SetNotifier(func(Update) { t.Fatal("alerted") })
	s.announce(context.Background(), uuid.New(), "v1", nil)
	if len(st.notices) != 0 {
		t.Errorf("recorded %v", st.notices)
	}
}
