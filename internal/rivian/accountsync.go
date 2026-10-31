package rivian

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// An Account lives in memory, one per user per pod. The truth about it (the stored
// session and the needs_reauth flag) lives in Postgres, and any pod can change it: a
// sign-in, a code for the second step, a sign-out or a flag raised by a pod that saw
// Rivian reject the session. AccountSyncer brings every pod's copy back in line with
// the database, so a sign-in on one pod is the sign-in on all of them.
//
// How: each user's state is a small version, the updated_at of the stored session row
// (a row that is deleted reads as "none") plus the flag and its reason. Reading it is
// one indexed query. The syncer runs it
//   - for every loaded account on a timer (SyncAll, a few seconds), which is how a pod
//     that serves no web traffic, such as the one holding a vehicle's lease, learns of a
//     new sign-in; and
//   - for one user right before the registry hands out that user's account (Touch, at
//     most once per minGap per user), which is how a web request sees a change at once.
//
// This is a poll, not LISTEN/NOTIFY, for the reasons internal/flags gives: a dedicated
// connection per pod and a reconnect path to lose notifications on, for a change that
// needs seconds, not milliseconds.
type AccountSyncer struct {
	src    AccountStateSource
	get    func(uuid.UUID) Account
	log    *slog.Logger
	minGap time.Duration
	now    func() time.Time

	mu       sync.Mutex
	seen     map[uuid.UUID]*syncSeen
	onChange func(uuid.UUID, SyncChange)
	// forget evicts an account from the registry (set by AttachSyncer).
	forget func(uuid.UUID)
	// onReconcile, when set, gets what each SyncAll found left over after correcting.
	onReconcile func(Reconcile)
}

// Reconcile is what a SyncAll round found: how many loaded accounts still disagreed with
// the database after the round tried to correct them (by field), and whether the database
// could not be read at all. Sustained non-zero values mean the sync is not converging.
type Reconcile struct {
	Accounts        int
	SessionMismatch int
	FlagMismatch    int
	ReadFailed      bool
}

// OnReconcile installs the callback for Reconcile results (one per SyncAll).
func (s *AccountSyncer) OnReconcile(fn func(Reconcile)) {
	s.mu.Lock()
	s.onReconcile = fn
	s.mu.Unlock()
}

// AccountDBState is what the database says about one user's Rivian account.
type AccountDBState struct {
	// SessionVersion is the updated_at of the stored session; zero when none is stored.
	SessionVersion time.Time
	NeedsReauth    bool
	Reason         string
}

// AccountStateSource reads the shared state. The Postgres-backed one is in package secrets.
type AccountStateSource interface {
	// States returns the state of each user asked about; a user it has no row for is
	// left out of the map.
	States(ctx context.Context, uids []uuid.UUID) (map[uuid.UUID]AccountDBState, error)
	// Session returns the stored session, the zero value when there is none.
	Session(ctx context.Context, uid uuid.UUID) (Session, error)
}

// SyncChange says what the syncer did to an account, for the pod's other parts (the
// monitor for that user) to follow.
type SyncChange int

const (
	// SyncSessionReplaced: another pod stored a different session and this pod now
	// holds it. A monitor running on the old tokens should restart on the new ones.
	SyncSessionReplaced SyncChange = iota + 1
	// SyncLoggedOut: the session was removed elsewhere and the local copy dropped.
	SyncLoggedOut
	// SyncUserRemoved: the users row is gone (the account was deleted): the account was dropped
	// from the registry. The monitor stops, and whatever the pod holds of the user in Redis is
	// theirs to delete (after the monitor is stopped, or it writes it again).
	SyncUserRemoved
)

type syncSeen struct {
	applied  bool   // the session version has been reconciled at least once
	restored string // the session token this syncer last put into the account itself
	version  time.Time
	checking bool
	checked  time.Time
}

// loginGrace is how long a sign-in made on this pod is trusted over a database that does
// not show it yet: the client holds the new tokens a moment before the handler stores
// them. It does not apply to a session the syncer itself copied from the database.
// Consequence, accepted: a sign-out made elsewhere within this long of a sign-in on this
// same pod is followed only once it has passed.
const loginGrace = 10 * time.Second

// NewAccountSyncer builds a syncer. get returns the account for a user WITHOUT
// triggering a sync (the registry's own lookup), or nil.
func NewAccountSyncer(src AccountStateSource, get func(uuid.UUID) Account, log *slog.Logger) *AccountSyncer {
	if log == nil {
		log = slog.Default()
	}
	return &AccountSyncer{
		src:    src,
		get:    get,
		log:    log,
		minGap: 2 * time.Second,
		now:    time.Now,
		seen:   make(map[uuid.UUID]*syncSeen),
	}
}

// OnChange installs the callback for SyncSessionReplaced and SyncLoggedOut. It runs
// after the account was updated and outside the syncer's lock.
func (s *AccountSyncer) OnChange(fn func(uuid.UUID, SyncChange)) {
	s.mu.Lock()
	s.onChange = fn
	s.mu.Unlock()
}

// Fresh reconciles one user's account now (no rate limit) and returns the state it read, for
// the user-facing status: whether the user needs to sign in again is answered from the
// database row, not from what this pod last remembered. ok is false when the database
// could not be read; the caller then falls back to the pod's copy.
func (s *AccountSyncer) Fresh(ctx context.Context, uid uuid.UUID) (AccountDBState, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	states, err := s.src.States(ctx, []uuid.UUID{uid})
	if err != nil {
		s.log.Warn("rivian account sync: read state", "user_id", uid.String(), "err", err.Error())
		return AccountDBState{}, false
	}
	s.mu.Lock()
	e := s.entry(uid)
	busy := e.checking
	if !busy {
		e.checking = true
	}
	s.mu.Unlock()
	if !busy {
		s.finish(uid, s.apply(ctx, uid, states))
	}
	st, ok := states[uid]
	return st, ok
}

// Superseded reports whether the stored session is a different one from usedSess, the
// u-sess a rejected call was made with. A rejection of a session that was replaced
// elsewhere is not the user's session dying, so it must not raise needs_reauth. When it
// is superseded, this account is also brought up to date in the background. It does not
// touch the account itself: it runs with the client lock held.
func (s *AccountSyncer) Superseded(ctx context.Context, uid uuid.UUID, usedSess string) bool {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	sess, err := s.src.Session(rctx, uid)
	if err != nil || sess.UserSessionToken == "" || sess.UserSessionToken == usedSess {
		return false
	}
	go func() {
		bctx, bcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer bcancel()
		s.Fresh(bctx, uid)
	}()
	return true
}

// Touch reconciles one user's account if it was not checked within minGap. Failures are
// logged and leave the account as it was: a database blip must not sign anyone out.
func (s *AccountSyncer) Touch(uid uuid.UUID) {
	s.mu.Lock()
	e := s.entry(uid)
	if e.checking || s.now().Sub(e.checked) < s.minGap {
		s.mu.Unlock()
		return
	}
	e.checking = true
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	states, err := s.src.States(ctx, []uuid.UUID{uid})
	if err != nil {
		s.log.Warn("rivian account sync: read state", "user_id", uid.String(), "err", err.Error())
		s.finish(uid, false)
		return
	}
	s.finish(uid, s.apply(ctx, uid, states))
}

// SyncAll reconciles every account the registry has loaded.
func (s *AccountSyncer) SyncAll(ctx context.Context, uids []uuid.UUID) {
	if len(uids) == 0 {
		return
	}
	states, err := s.src.States(ctx, uids)
	if err != nil {
		s.log.Warn("rivian account sync: read states", "users", len(uids), "err", err.Error())
		s.reconciled(Reconcile{Accounts: len(uids), ReadFailed: true})
		return
	}
	for _, uid := range uids {
		s.mu.Lock()
		e := s.entry(uid)
		busy := e.checking
		if !busy {
			e.checking = true
		}
		s.mu.Unlock()
		if busy {
			continue
		}
		s.finish(uid, s.apply(ctx, uid, states))
	}
	s.reconciled(s.residual(uids, states))
}

func (s *AccountSyncer) reconciled(r Reconcile) {
	s.mu.Lock()
	fn := s.onReconcile
	s.mu.Unlock()
	if fn != nil {
		fn(r)
	}
}

// residual counts the loaded accounts that still disagree with the states just read: the
// flag differs, or the session is signed in against nothing stored (or the other way
// round), or this pod knows the session by another version. A sign-in made on this pod
// moments ago is not a disagreement yet (see loginGrace).
func (s *AccountSyncer) residual(uids []uuid.UUID, states map[uuid.UUID]AccountDBState) Reconcile {
	r := Reconcile{Accounts: len(uids)}
	now := s.now()
	for _, uid := range uids {
		st, ok := states[uid]
		a := s.get(uid)
		if !ok || a == nil {
			continue
		}
		if cur, _ := a.NeedsReauth(); cur != st.NeedsReauth {
			r.FlagMismatch++
		}
		local := a.Snapshot()
		if now.Sub(local.AuthenticatedAt) < loginGrace {
			continue
		}
		stored := !st.SessionVersion.IsZero()
		switch {
		case stored != a.Authenticated():
			r.SessionMismatch++
		case stored:
			if vs, ok := a.(interface{ SessionVersion() time.Time }); ok {
				if v := vs.SessionVersion(); !v.Equal(st.SessionVersion) {
					r.SessionMismatch++
				}
			}
		}
	}
	return r
}

// Run calls SyncAll(loaded()) every interval until ctx ends.
func (s *AccountSyncer) Run(ctx context.Context, interval time.Duration, loaded func() []uuid.UUID) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.SyncAll(ctx, loaded())
		}
	}
}

func (s *AccountSyncer) entry(uid uuid.UUID) *syncSeen {
	e, ok := s.seen[uid]
	if !ok {
		e = &syncSeen{}
		s.seen[uid] = e
	}
	return e
}

func (s *AccountSyncer) finish(uid uuid.UUID, checked bool) {
	s.mu.Lock()
	e := s.entry(uid)
	e.checking = false
	if checked {
		e.checked = s.now()
	}
	s.mu.Unlock()
}

type sessionVersioner interface {
	SetSessionVersion(time.Time)
}

type reauthSetter interface {
	SetNeedsReauth(needs bool, reason string)
}

// apply reconciles one account with the state read for it. It returns false when the
// account could not be reconciled this round (nothing is marked as seen, so the next
// round tries again).
func (s *AccountSyncer) apply(ctx context.Context, uid uuid.UUID, states map[uuid.UUID]AccountDBState) bool {
	st, ok := states[uid]
	a := s.get(uid)
	if a == nil {
		return true
	}
	if !ok {
		// The users row is gone (the account was deleted): drop the account and its
		// tokens from this pod, and stop its monitor.
		a.Logout()
		if s.forget != nil {
			s.forget(uid)
		}
		s.mu.Lock()
		delete(s.seen, uid)
		fn := s.onChange
		s.mu.Unlock()
		s.log.Info("rivian account sync: user removed, account dropped", "user_id", uid.String())
		if fn != nil {
			fn(uid, SyncUserRemoved)
		}
		return true
	}

	s.mu.Lock()
	e := s.entry(uid)
	versionChanged := !e.applied || !e.version.Equal(st.SessionVersion)
	s.mu.Unlock()

	change := SyncChange(0)
	retry := false // the session could not be reconciled this round: try again next round
	if versionChanged {
		local := a.Snapshot()
		inSync := false // this pod's session is the stored one (so it may know it by that version)
		if st.SessionVersion.IsZero() {
			// Nothing stored. A sign-in finished a moment ago on this pod may not be stored
			// yet; otherwise another pod signed out and this one must follow.
			if a.Authenticated() {
				if s.signedInHere(e, local) {
					retry = true
				} else {
					a.Logout()
					change = SyncLoggedOut
				}
			}
		} else if sess, err := s.src.Session(ctx, uid); err != nil {
			s.log.Warn("rivian account sync: load session", "user_id", uid.String(), "err", err.Error())
			retry = true
		} else {
			switch {
			case sess.UserSessionToken == "":
				if a.Authenticated() && !s.signedInHere(e, local) {
					a.Logout()
					change = SyncLoggedOut
				}
			case sess.UserSessionToken == local.UserSessionToken:
				inSync = true
			case !local.AuthenticatedAt.After(sess.AuthenticatedAt):
				a.Restore(sess)
				s.mu.Lock()
				e.restored = sess.UserSessionToken
				s.mu.Unlock()
				change = SyncSessionReplaced
				inSync = true
			}
		}
		if !retry {
			s.mu.Lock()
			e.applied, e.version = true, st.SessionVersion
			s.mu.Unlock()
		}
		// This pod knows its session by the stored version only when it holds that session:
		// a stale 401 of an older one must still be recognised as stale (A3).
		if vs, ok := a.(sessionVersioner); ok && inSync {
			vs.SetSessionVersion(st.SessionVersion)
		}
	}

	// A client that lost or never recorded its version (a sign-in made on this pod, stored
	// since) learns it once the stored session is the one it holds: the round that found the
	// version changed has already reconciled the tokens.
	if !versionChanged && !st.SessionVersion.IsZero() {
		if vs, ok := a.(interface {
			sessionVersioner
			SessionVersion() time.Time
		}); ok && vs.SessionVersion().IsZero() {
			// The stored session is the one this client holds when the tokens are the same: no clock is compared (the stored version is
			// the database's, a pod's AuthenticatedAt is the pod's, and they may disagree by the skew between them).
			if local := a.Snapshot(); local.UserSessionToken != "" {
				if sess, err := s.src.Session(ctx, uid); err == nil && sess.UserSessionToken == local.UserSessionToken {
					vs.SetSessionVersion(st.SessionVersion)
				}
			}
		}
	}

	// The database is the truth for the flag: any mismatch is corrected, every round (a
	// flag a pod raised but the database refused, or never got, does not outlive the next
	// sync).
	if rs, ok := a.(reauthSetter); ok {
		cur, curReason := a.NeedsReauth()
		if cur != st.NeedsReauth || (st.NeedsReauth && curReason != st.Reason) {
			rs.SetNeedsReauth(st.NeedsReauth, st.Reason)
		}
	}

	if change != 0 {
		s.log.Info("rivian account sync: followed another pod", "user_id", uid.String(), "change", change.String())
		s.mu.Lock()
		fn := s.onChange
		s.mu.Unlock()
		if fn != nil {
			fn(uid, change)
		}
	}
	return !retry
}

// signedInHere reports whether the account's session was made on this pod moments ago
// (and not copied from the database by this syncer), so a database that does not show it
// yet is not evidence of a sign-out.
func (s *AccountSyncer) signedInHere(e *syncSeen, local Session) bool {
	s.mu.Lock()
	restored := e.restored
	s.mu.Unlock()
	return local.UserSessionToken != restored && s.now().Sub(local.AuthenticatedAt) < loginGrace
}

func (c SyncChange) String() string {
	switch c {
	case SyncSessionReplaced:
		return "session_replaced"
	case SyncLoggedOut:
		return "logged_out"
	case SyncUserRemoved:
		return "user_removed"
	}
	return "none"
}
