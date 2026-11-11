package rivian

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// answers is a hold predicate that answers per user: held, known.
type answers struct {
	mu sync.Mutex
	m  map[uuid.UUID][2]bool
}

func (a *answers) set(uid uuid.UUID, held, known bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.m[uid] = [2]bool{held, known}
}

func (a *answers) hold(_ context.Context, uid uuid.UUID) (bool, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.m[uid]
	return v[0], v[1]
}

func gatedRegistry(t *testing.T) (*liveAccountRegistry, *answers, *int) {
	t.Helper()
	built := 0
	reg := NewLiveAccountRegistry(func(uuid.UUID) *LiveClient {
		built++
		lc := NewLive()
		lc.Restore(Session{UserSessionToken: "stored", CSRFToken: "c", AppSessionToken: "a"})
		return lc
	}).(*liveAccountRegistry)
	a := &answers{m: map[uuid.UUID][2]bool{}}
	reg.SetNoStandingTokens(a.hold, func(uuid.UUID) *LiveClient { return NewLive() })
	return reg, a, &built
}

// For, Raw and the lazy build answer a literal nil for a group the pod does not hold: nil as an interface (a typed nil would pass a != nil
// check and panic at the first call), nothing built (so nothing restored), nothing stored.
func TestGatedRegistryAnswersLiteralNilForUnheld(t *testing.T) {
	reg, a, built := gatedRegistry(t)
	unheld, held := uuid.New(), uuid.New()
	a.set(unheld, false, true)
	a.set(held, true, true)

	var acct Account = reg.For(unheld)
	if acct != nil {
		t.Fatalf("For(unheld) = %#v, want a nil interface", acct)
	}
	var raw Account = reg.Raw(unheld)
	if raw != nil {
		t.Fatalf("Raw(unheld) = %#v, want a nil interface", raw)
	}
	if *built != 0 {
		t.Fatalf("a client was built for an unheld user (%d)", *built)
	}
	if got := reg.Loaded(); len(got) != 0 {
		t.Fatalf("Loaded() = %v after asking for an unheld user, want none", got)
	}
	if reg.For(held) == nil || len(reg.Loaded()) != 1 {
		t.Fatal("a held user must get a stored client")
	}
	// A lookup that cannot be answered is not held, whatever it said.
	failed := uuid.New()
	a.set(failed, false, false)
	if reg.For(failed) != nil {
		t.Fatal("For with an unanswerable lookup must not hand out a client")
	}
}

// A client that was handed out while the group was held is not handed out once it is not.
func TestGatedRegistryStopsHandingOutALostGroup(t *testing.T) {
	reg, a, _ := gatedRegistry(t)
	uid := uuid.New()
	a.set(uid, true, true)
	if reg.For(uid) == nil {
		t.Fatal("held: want a client")
	}
	a.set(uid, false, true)
	var acct Account = reg.For(uid)
	if acct != nil {
		t.Fatal("For after the group was lost still hands out the client")
	}
}

// ReleaseUnheld wipes and drops a client whose group is known to be lost, keeps a held one, and keeps one whose group could not be looked up
// (a database blip must not strip the owner of its tokens).
func TestReleaseUnheldZeroesOnlyWhatIsKnownLost(t *testing.T) {
	reg, a, _ := gatedRegistry(t)
	lost, kept, blip := uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{lost, kept, blip} {
		a.set(u, true, true)
		reg.For(u)
	}
	lostClient := reg.users[lost]
	a.set(lost, false, true)
	a.set(blip, false, false)

	if n := reg.ReleaseUnheld(context.Background()); n != 1 {
		t.Fatalf("ReleaseUnheld = %d, want 1", n)
	}
	if lostClient.Authenticated() || lostClient.Snapshot().CSRFToken != "" || lostClient.Snapshot().AppSessionToken != "" {
		t.Fatal("the lost group's client still holds a credential")
	}
	loaded := map[uuid.UUID]bool{}
	for _, u := range reg.Loaded() {
		loaded[u] = true
	}
	if loaded[lost] || !loaded[kept] || !loaded[blip] {
		t.Fatalf("loaded = %v, want the held and the unknown kept and the lost dropped", loaded)
	}
	if !reg.users[kept].Authenticated() || !reg.users[blip].Authenticated() {
		t.Fatal("a held or unknown group's client was wiped")
	}
}

// A sign-in on a pod that does not hold the account works on a throwaway client that restores no stored session, is not in the registry,
// and is wiped by done.
func TestForLoginOnAnUnheldPodLeavesNothing(t *testing.T) {
	reg, a, built := gatedRegistry(t)
	uid := uuid.New()
	a.set(uid, false, true)
	acct, done := ForLogin(reg, uid)
	lc, ok := acct.(*LiveClient)
	if !ok || lc == nil {
		t.Fatalf("ForLogin = %#v, want a client", acct)
	}
	if lc.Authenticated() {
		t.Fatal("the sign-in client restored a stored session")
	}
	if *built != 0 || len(reg.Loaded()) != 0 {
		t.Fatalf("the sign-in client went through the registry (built %d, loaded %v)", *built, reg.Loaded())
	}
	lc.Restore(Session{UserSessionToken: "new", CSRFToken: "c", AppSessionToken: "a"})
	done()
	if lc.Authenticated() || lc.Snapshot().CSRFToken != "" || lc.Snapshot().AppSessionToken != "" {
		t.Fatal("done left a credential on the sign-in client")
	}
}

// For a held user ForLogin is the registered client and done keeps it.
func TestForLoginOnAHeldPodIsTheRegisteredClient(t *testing.T) {
	reg, a, _ := gatedRegistry(t)
	uid := uuid.New()
	a.set(uid, true, true)
	acct, done := ForLogin(reg, uid)
	done()
	if acct != reg.For(uid) || !acct.Authenticated() {
		t.Fatal("a held user's sign-in must use, and keep, the registered client")
	}
}

// Without the gate the registry is what it was: every user gets a stored client.
func TestUngatedRegistryUnchanged(t *testing.T) {
	reg := NewLiveAccountRegistry(func(uuid.UUID) *LiveClient { return NewLive() }).(*liveAccountRegistry)
	if NoStandingTokens(reg) || reg.For(uuid.New()) == nil || reg.Raw(uuid.New()) == nil || len(reg.Loaded()) != 2 {
		t.Fatal("an ungated registry changed behaviour")
	}
	if n := reg.ReleaseUnheld(context.Background()); n != 0 {
		t.Fatalf("ReleaseUnheld on an ungated registry dropped %d", n)
	}
}

func TestUnheldClientAnswersAnOutage(t *testing.T) {
	c := NewUnheldClient()
	_, err := c.Vehicles(context.Background())
	ue, ok := err.(*UpstreamError)
	if !ok || ue.Class != ClassOutage {
		t.Fatalf("Vehicles error = %#v, want an outage UpstreamError (503)", err)
	}
}

func gatedReplay(t *testing.T) (*replayAccountRegistry, *answers) {
	t.Helper()
	feed, _ := testFeed(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC), time.Hour)
	reg := NewReplayAccountRegistry(feed, nil).(*replayAccountRegistry)
	a := &answers{m: map[uuid.UUID][2]bool{}}
	reg.SetNoStandingTokens(a.hold, nil)
	return reg, a
}

// The replay registry (preview) runs the same gate: a literal nil for an unheld or unanswerable user, nothing registered; a held user gets a
// client; ForLogin gives an unheld user a client that is not registered.
func TestGatedReplayRegistry(t *testing.T) {
	reg, a := gatedReplay(t)
	if !NoStandingTokens(reg) {
		t.Fatal("the replay registry did not report the gate")
	}
	unheld, held, blip := uuid.New(), uuid.New(), uuid.New()
	a.set(unheld, false, true)
	a.set(held, true, true)
	a.set(blip, false, false)
	for _, u := range []uuid.UUID{unheld, blip} {
		var acct Account = reg.For(u)
		if acct != nil {
			t.Fatalf("For(%v) = %#v, want a nil interface", u, acct)
		}
	}
	if reg.For(held) == nil {
		t.Fatal("a held user must get a client")
	}
	if got := reg.Loaded(); len(got) != 1 || got[0] != held {
		t.Fatalf("Loaded() = %v, want only the held user", got)
	}
	acct, done := ForLogin(reg, unheld)
	done()
	if acct == nil || len(reg.Loaded()) != 1 {
		t.Fatalf("ForLogin on an unheld user: %#v, loaded %v; want an unregistered client", acct, reg.Loaded())
	}
}

func TestGatedReplayReleaseUnheld(t *testing.T) {
	reg, a := gatedReplay(t)
	lost, kept, blip := uuid.New(), uuid.New(), uuid.New()
	for _, u := range []uuid.UUID{lost, kept, blip} {
		a.set(u, true, true)
		reg.For(u)
	}
	a.set(lost, false, true)
	a.set(blip, false, false)
	if n := reg.ReleaseUnheld(context.Background()); n != 1 || len(reg.Loaded()) != 2 {
		t.Fatalf("ReleaseUnheld = %d, loaded %v; want the lost group dropped and the rest kept", n, reg.Loaded())
	}
}

func TestUngatedReplayRegistryUnchanged(t *testing.T) {
	feed, _ := testFeed(time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC), time.Hour)
	reg := NewReplayAccountRegistry(feed, nil).(*replayAccountRegistry)
	if NoStandingTokens(reg) || reg.For(uuid.New()) == nil || reg.ReleaseUnheld(context.Background()) != 0 {
		t.Fatal("an ungated replay registry changed behaviour")
	}
}
