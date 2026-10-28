package rivian

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// fakeShared is the database two pods share.
type fakeShared struct {
	mu      sync.Mutex
	version time.Time
	sess    Session
	needs   bool
	reason  string
	err     error
	reads   int
}

// signIn is what the login handler on some pod does: store the session and clear the flag.
func (f *fakeShared) signIn(sess Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sess, f.version, f.needs, f.reason = sess, f.version.Add(time.Second).Add(time.Minute), false, ""
}

func (f *fakeShared) signOut() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sess, f.version = Session{}, time.Time{}
}

func (f *fakeShared) flag(reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.needs, f.reason = reason != "", reason
}

func (f *fakeShared) States(_ context.Context, uids []uuid.UUID) (map[uuid.UUID]AccountDBState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	if f.err != nil {
		return nil, f.err
	}
	out := map[uuid.UUID]AccountDBState{}
	for _, u := range uids {
		out[u] = AccountDBState{SessionVersion: f.version, NeedsReauth: f.needs, Reason: f.reason}
	}
	return out, nil
}

func (f *fakeShared) Session(context.Context, uuid.UUID) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sess, nil
}

// podB is a second pod: its own registry over the same shared state, built the way
// buildLive does (restore the stored session and flag at construction).
func podB(shared *fakeShared, uid uuid.UUID) (AccountRegistry, *AccountSyncer, *[]SyncChange) {
	reg := NewLiveAccountRegistry(func(uuid.UUID) *LiveClient {
		lc := NewLive()
		shared.mu.Lock()
		if shared.sess.UserSessionToken != "" {
			lc.Restore(shared.sess)
		}
		if shared.needs {
			lc.SetNeedsReauth(true, shared.reason)
		}
		shared.mu.Unlock()
		return lc
	})
	changes := &[]SyncChange{}
	syncer := NewAccountSyncer(shared, reg.(interface{ Raw(uuid.UUID) Account }).Raw, nil)
	syncer.minGap = 0
	syncer.OnChange(func(_ uuid.UUID, c SyncChange) { *changes = append(*changes, c) })
	reg.(interface{ AttachSyncer(*AccountSyncer) }).AttachSyncer(syncer)
	return reg, syncer, changes
}

func sessionOf(token string, at time.Time) Session {
	return Session{Email: "a@example.invalid", UserSessionToken: token, CSRFToken: "c-" + token, AppSessionToken: "a-" + token, AuthenticatedAt: at}
}

func TestSyncerSignInOnOnePodIsSeenAtOnceOnTheOther(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	old := sessionOf("old", time.Now().Add(-180*24*time.Hour))
	shared.signIn(old)
	shared.flag("session expired") // the old session died and one pod flagged it

	b, _, changes := podB(shared, uid)
	acct := b.For(uid)
	if needs, _ := acct.NeedsReauth(); !needs || acct.Snapshot().UserSessionToken != "old" {
		t.Fatalf("pod B did not start from the stored dead session: needs=%v token=%q", needs, acct.Snapshot().UserSessionToken)
	}

	// Pod A: the user signs in again (new session stored, flag cleared).
	shared.signIn(sessionOf("new", time.Now()))

	acct = b.For(uid) // the next request on pod B, no timer needed
	if needs, why := acct.NeedsReauth(); needs {
		t.Fatalf("pod B still says needs_reauth (%q) after the sign-in on pod A", why)
	}
	if !acct.Authenticated() || acct.Snapshot().UserSessionToken != "new" {
		t.Fatalf("pod B does not hold the new session: authenticated=%v token=%q", acct.Authenticated(), acct.Snapshot().UserSessionToken)
	}
	if len(*changes) != 1 || (*changes)[0] != SyncSessionReplaced {
		t.Fatalf("changes = %v, want one SyncSessionReplaced (the monitor restarts on the new tokens)", *changes)
	}
}

func TestSyncerSignOutOnOnePodReachesTheOther(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("tok", time.Now().Add(-time.Hour)))
	b, _, changes := podB(shared, uid)
	if !b.For(uid).Authenticated() {
		t.Fatal("pod B should start signed in")
	}
	shared.signOut()
	if b.For(uid).Authenticated() {
		t.Fatal("pod B still holds the session after the sign-out on pod A")
	}
	if len(*changes) != 1 || (*changes)[0] != SyncLoggedOut {
		t.Fatalf("changes = %v, want SyncLoggedOut (the monitor stops)", *changes)
	}
}

func TestSyncerFlagRaisedOnOnePodReachesTheOtherAndClears(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("tok", time.Now().Add(-time.Hour)))
	b, _, changes := podB(shared, uid)
	b.For(uid)

	shared.flag("session expired")
	if needs, why := b.For(uid).NeedsReauth(); !needs || why != "session expired" {
		t.Fatalf("flag did not reach pod B: %v %q", needs, why)
	}
	shared.flag("")
	if needs, _ := b.For(uid).NeedsReauth(); needs {
		t.Fatal("cleared flag did not reach pod B")
	}
	if len(*changes) != 0 {
		t.Fatalf("a flag change alone must not restart monitors: %v", *changes)
	}
}

func TestSyncerKeepsAFreshLocalSignInThatIsNotStoredYet(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{} // nothing stored
	b, syn, changes := podB(shared, uid)
	acct := b.For(uid).(*LiveClient)
	acct.Restore(sessionOf("just-now", time.Now())) // the login handler's Login() has returned, Save has not run
	syn.Touch(uid)
	if !acct.Authenticated() || len(*changes) != 0 {
		t.Fatal("a sign-in finished seconds ago was dropped because the store does not show it yet")
	}
	// It is retried: once stored, nothing changes either.
	shared.signIn(sessionOf("just-now", time.Now()))
	syn.Touch(uid)
	if !acct.Authenticated() || acct.Snapshot().UserSessionToken != "just-now" {
		t.Fatal("session lost after it was stored")
	}
}

func TestSyncerDoesNotReplaceANewerLocalSessionWithAnOlderStoredOne(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("stored-old", time.Now().Add(-time.Hour)))
	b, syn, _ := podB(shared, uid)
	acct := b.For(uid).(*LiveClient)
	acct.Restore(sessionOf("local-new", time.Now()))
	shared.signIn(sessionOf("stored-old", time.Now().Add(-time.Hour))) // a version bump with an older session
	syn.Touch(uid)
	if acct.Snapshot().UserSessionToken != "local-new" {
		t.Fatal("a newer local session was replaced by an older stored one")
	}
}

func TestSyncerLeavesTheAccountAloneWhenTheDatabaseFails(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("tok", time.Now().Add(-time.Hour)))
	b, _, changes := podB(shared, uid)
	b.For(uid)
	shared.mu.Lock()
	shared.err = errors.New("connection refused")
	shared.version = time.Time{} // would read as a sign-out
	shared.mu.Unlock()
	if !b.For(uid).Authenticated() || len(*changes) != 0 {
		t.Fatal("a database error signed the user out")
	}
}

func TestSyncerIsRateLimitedPerUser(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("tok", time.Now().Add(-time.Hour)))
	b, syn, _ := podB(shared, uid)
	syn.minGap = time.Hour
	for i := 0; i < 50; i++ {
		b.For(uid)
	}
	if shared.reads != 1 {
		t.Fatalf("%d database reads for 50 lookups, want 1", shared.reads)
	}
}

func TestSyncAllFollowsEveryLoadedAccount(t *testing.T) {
	u1, u2 := uuid.New(), uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("old", time.Now().Add(-time.Hour)))
	b, syn, changes := podB(shared, u1)
	syn.minGap = time.Hour // only SyncAll may act
	b.For(u1)
	b.For(u2)
	shared.signIn(sessionOf("new", time.Now()))
	syn.SyncAll(context.Background(), b.(interface{ Loaded() []uuid.UUID }).Loaded())
	for _, u := range []uuid.UUID{u1, u2} {
		if got := b.For(u).Snapshot().UserSessionToken; got != "new" {
			t.Fatalf("user %s holds %q after SyncAll, want new", u, got)
		}
	}
	if len(*changes) != 2 {
		t.Fatalf("changes = %v, want two", *changes)
	}
}

// A session this pod copied from the database is not "just signed in here": a sign-out
// elsewhere a few seconds after the sign-in must still reach it.
func TestSyncerFollowsASignOutRightAfterASignInOnAnotherPod(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	b, _, changes := podB(shared, uid)
	b.For(uid)
	shared.signIn(sessionOf("fresh", time.Now())) // signed in on pod A seconds ago
	if b.For(uid).Snapshot().UserSessionToken != "fresh" {
		t.Fatal("pod B did not follow the sign-in")
	}
	shared.signOut()
	if b.For(uid).Authenticated() {
		t.Fatal("pod B kept a session that was signed out seconds after it was made")
	}
	if len(*changes) != 2 || (*changes)[1] != SyncLoggedOut {
		t.Fatalf("changes = %v, want replaced then logged out", *changes)
	}
}
