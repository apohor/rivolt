package rivian

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	gone    bool // the users row was deleted
	sessErr error
}

// signIn is what the login handler on some pod does: store the session and clear the flag.
func (f *fakeShared) signIn(sess Session) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// updated_at is when it was stored: never earlier than the previous write.
	v := time.Now()
	if !v.After(f.version) {
		v = f.version.Add(time.Second)
	}
	f.sess, f.version, f.needs, f.reason = sess, v, false, ""
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
	if f.gone {
		return out, nil
	}
	for _, u := range uids {
		out[u] = AccountDBState{SessionVersion: f.version, NeedsReauth: f.needs, Reason: f.reason}
	}
	return out, nil
}

func (f *fakeShared) Session(context.Context, uuid.UUID) (Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessErr != nil {
		return Session{}, f.sessErr
	}
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

// A3: a stale client (it still holds the replaced session) is rejected by Rivian right
// after the user signed in again elsewhere. That must not raise needs_reauth or mail
// the user; a rejection of the session that IS the stored one still does.
func TestRejectionOfAReplacedSessionDoesNotRaiseNeedsReauth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"errors":[{"message":"session expired"}]}`)
	}))
	defer server.Close()
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("replaced", time.Now().Add(-time.Hour)))
	syn := NewAccountSyncer(shared, func(uuid.UUID) Account { return nil }, nil)

	call := func(token string) (*LiveClient, int) {
		sinks := 0
		c := NewLive()
		c.endpoint = server.URL + "/gateway"
		c.WithReauthSink(func(context.Context, string) { sinks++ })
		c.WithReauthGuard(func(ctx context.Context, used string) bool { return syn.Superseded(ctx, uid, used) })
		_, err := doGraphQLAt[map[string]any](context.Background(), c, c.endpoint,
			graphQLRequest{OperationName: "getUserInfo", Query: "query getUserInfo { ok }"},
			map[string]string{"u-sess": token})
		if err == nil {
			t.Fatal("expected the 401")
		}
		return c, sinks
	}

	// Signed in again elsewhere: the stored session is "current", the failing call used "replaced".
	shared.signIn(sessionOf("current", time.Now()))
	c, sinks := call("replaced")
	if needs, _ := c.NeedsReauth(); needs || sinks != 0 {
		t.Fatalf("a rejected replaced session raised needs_reauth (needs=%v, sink calls=%d)", needs, sinks)
	}
	// The stored session itself is rejected: that is the user's session dying.
	c, sinks = call("current")
	if needs, _ := c.NeedsReauth(); !needs || sinks != 1 {
		t.Fatalf("a rejected current session must raise needs_reauth (needs=%v, sink calls=%d)", needs, sinks)
	}
}

// A4: the users row is gone (account deleted): this pod drops the account's tokens and
// forgets it, and the monitor is told.
func TestSyncerDropsAnAccountWhoseUserWasDeleted(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("tok", time.Now().Add(-time.Hour)))
	reg, syn, changes := podB(shared, uid)
	acct := reg.For(uid)
	if !acct.Authenticated() {
		t.Fatal("setup: not signed in")
	}
	shared.mu.Lock()
	shared.gone = true
	shared.mu.Unlock()
	syn.Touch(uid)
	if acct.Authenticated() {
		t.Fatal("the deleted user's tokens are still held in memory")
	}
	for _, u := range reg.Loaded() {
		if u == uid {
			t.Fatal("the deleted user's account is still in the registry")
		}
	}
	if len(*changes) != 1 || (*changes)[0] != SyncUserRemoved {
		t.Fatalf("changes = %v, want SyncUserRemoved (the monitor stops and the user's Redis keys are deleted)", *changes)
	}
}

// Fresh answers from the stored row and brings the pod's copy along, with no rate limit.
func TestFreshReadsTheDatabaseOnEveryCall(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("tok", time.Now().Add(-time.Hour)))
	reg, syn, _ := podB(shared, uid)
	syn.minGap = time.Hour // For would not re-read; Fresh must
	reg.For(uid)
	shared.flag("session expired")
	st, ok := reg.(interface {
		Fresh(context.Context, uuid.UUID) (AccountDBState, bool)
	}).Fresh(context.Background(), uid)
	if !ok || !st.NeedsReauth || st.Reason != "session expired" {
		t.Fatalf("Fresh = %+v ok=%v, want the stored flag", st, ok)
	}
	if needs, _ := reg.For(uid).NeedsReauth(); !needs {
		t.Fatal("the pod's copy was not brought along by Fresh")
	}
	shared.flag("")
	if st, _ := reg.(interface {
		Fresh(context.Context, uuid.UUID) (AccountDBState, bool)
	}).Fresh(context.Background(), uid); st.NeedsReauth {
		t.Fatal("Fresh served a stale flag")
	}
}

// A pod that cannot converge says so: after a SyncAll that could not load the new session
// the account still disagrees with the database, and the reconcile report counts it by
// field. When the fault clears, the next round reports zero.
func TestSyncAllReportsWhatItCouldNotReconcile(t *testing.T) {
	uid := uuid.New()
	shared := &fakeShared{}
	shared.signIn(sessionOf("old", time.Now().Add(-time.Hour)))
	reg, syn, _ := podB(shared, uid)
	syn.minGap = time.Hour
	reg.For(uid)
	var got []Reconcile
	syn.OnReconcile(func(r Reconcile) { got = append(got, r) })

	syn.SyncAll(context.Background(), reg.Loaded())
	if len(got) != 1 || got[0] != (Reconcile{Accounts: 1}) {
		t.Fatalf("in sync: %+v, want one clean report", got)
	}

	// Another pod signs in and flags; this pod's copy cannot be updated (the session load fails).
	shared.signIn(sessionOf("new", time.Now()))
	shared.mu.Lock()
	shared.sessErr = errors.New("kms unavailable")
	shared.mu.Unlock()
	syn.SyncAll(context.Background(), reg.Loaded())
	if got[1].SessionMismatch != 1 || got[1].FlagMismatch != 0 {
		t.Fatalf("session could not be loaded: %+v, want one session mismatch", got[1])
	}
	// Holding the old session, this pod must not claim the new session's version: a stale
	// rejection of the old one has to stay recognisable as stale (A3).
	shared.mu.Lock()
	newVersion := shared.version
	shared.mu.Unlock()
	if v := reg.For(uid).(*LiveClient).SessionVersion(); v.Equal(newVersion) {
		t.Fatal("the pod took the new session's version without holding the new session")
	}
	shared.flag("session expired")
	syn.SyncAll(context.Background(), reg.Loaded())
	if got[2].SessionMismatch != 1 {
		t.Fatalf("still failing: %+v", got[2])
	}
	if got[2].FlagMismatch != 0 {
		t.Fatalf("the flag is applied even while the session cannot load, got %+v", got[2])
	}

	shared.mu.Lock()
	shared.sessErr = nil
	shared.mu.Unlock()
	syn.SyncAll(context.Background(), reg.Loaded())
	if got[3].SessionMismatch != 0 || got[3].FlagMismatch != 0 {
		t.Fatalf("after the fault cleared: %+v, want 0/0", got[3])
	}
	if v := reg.For(uid).(*LiveClient).SessionVersion(); !v.Equal(newVersion) {
		t.Fatalf("after the new session was loaded the pod knows it by %v, want %v", v, newVersion)
	}

	// And a database that cannot be read at all is its own signal.
	shared.mu.Lock()
	shared.err = errors.New("connection refused")
	shared.mu.Unlock()
	syn.SyncAll(context.Background(), reg.Loaded())
	if !got[4].ReadFailed {
		t.Fatalf("unreadable database: %+v, want ReadFailed", got[4])
	}
}

// A version belongs to the session it was learnt for. A call that used another token (one still in flight when the client followed a sign-in
// made elsewhere and relearnt the new version) gets no version: its rejection must not be attributed to the new session.
func TestSessionVersionIsForTheSessionItWasLearntFor(t *testing.T) {
	c := NewLive()
	c.Restore(Session{Email: "d@example.invalid", UserSessionToken: "old", AuthenticatedAt: time.Now().Add(-time.Hour)})
	v1 := time.Date(2026, 11, 1, 10, 0, 0, 123456000, time.UTC)
	c.SetSessionVersion(v1)
	if got := c.SessionVersionFor("old"); !got.Equal(v1) {
		t.Fatalf("version for the token it was learnt for = %v, want %v", got, v1)
	}
	if got := c.SessionVersion(); !got.Equal(v1) {
		t.Errorf("SessionVersion() = %v", got)
	}
	// The client follows a sign-in elsewhere: new token, new version.
	c.Restore(Session{Email: "d@example.invalid", UserSessionToken: "fresh", AuthenticatedAt: time.Now()})
	v2 := v1.Add(time.Minute)
	c.SetSessionVersion(v2)
	if got := c.SessionVersionFor("fresh"); !got.Equal(v2) {
		t.Errorf("version for the fresh token = %v, want %v", got, v2)
	}
	if got := c.SessionVersionFor("old"); !got.IsZero() {
		t.Errorf("a call that used the OLD token is attributed version %v: its rejection would flag the fresh session", got)
	}
	if got := c.SessionVersionFor(""); !got.IsZero() {
		t.Errorf("an unknown token got version %v", got)
	}
	// Between the Restore and the relearn the client holds a new token with the old version: that is no version either.
	c.Restore(Session{Email: "d@example.invalid", UserSessionToken: "newest", AuthenticatedAt: time.Now()})
	if got := c.SessionVersionFor("newest"); !got.IsZero() {
		t.Errorf("a token the version was not learnt for got version %v", got)
	}
	c.SetSessionVersion(time.Time{})
	if !c.SessionVersion().IsZero() || !c.SessionVersionFor("newest").IsZero() {
		t.Error("clearing the version left one behind")
	}
}
