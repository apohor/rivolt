package forward

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/fence"
)

// Statuses and classes the requester itself produces (an owner's replies carry their own).
const (
	StatusUnavailable = 503
	ClassUnavailable  = "unavailable"
	StatusBadReply    = 502
	ClassBadReply     = "bad_reply"
	RetryAfterSeconds = 5 // the Retry-After a handler sends with a 503 from here
)

// LeaseForgetter is implemented by a LeaseView that caches: Forget drops a group's cached row so the next Owner call reads it fresh.
type LeaseForgetter interface{ Forget(group string) }

// RequesterConfig wires a Forwarder.
type RequesterConfig struct {
	Pod     string // this pod (the lease holder name)
	Keys    Keys
	Signer  *Signer
	Redis   redis.Cmdable
	Leases  LeaseView
	Metrics *Metrics
	Logger  *slog.Logger
	// Timeouts overrides a kind's wait (tests). Zero or missing: the kind's own.
	Timeouts map[Kind]time.Duration
	// Jitter is the pause before the one retry after a `busy` reply (default 200-500 ms).
	Jitter func() time.Duration
	// Acquire takes a group whose lease is absent or expired, by the database clock, and reports whether this pod now holds it (single-owner
	// M4-2, the acquire half). Nil, or AcquireOnRequest false: the M3 behaviour, the fail-soft cases run the call here.
	Acquire func(ctx context.Context, group string) (bool, error)
	// AcquireOnRequest (RIVOLT_ACQUIRE_ON_REQUEST) replaces the fail-soft "run it here" with acquire-or-503: the only trigger for an
	// acquire is a lease row absent or expired; a live lease (owner not ready, Redis down, busy, moved to someone) is a 503, never an acquire.
	AcquireOnRequest bool
	// NoLocal (a pod whose role holds no leases: RIVOLT_ROLE api, jobs) never runs a call on this pod and never acquires: every fall-soft
	// case, and an acquire, is a 503 Retry-After counted `no_local`. Such a pod holds no group, so a call it ran itself would be a
	// non-owner Rivian call.
	NoLocal bool
	// OwnerWait is the longest a call waits for a draining owner's successor: the owner has written "draining" in its readiness marker
	// (it stops taking new work at SIGUSR1 but keeps its lease rows until its subscriptions are stopped and released), so the call waits,
	// before anything is sent, for a ready owner to appear, and goes to it. A missing marker (a crash, an old image) never waits. Each wait is
	// also capped by what the request's deadline leaves after the kind's own timeout (WaitBudget). Zero: no wait. Only a pod that could not
	// serve the call itself (NoLocal, or AcquireOnRequest) waits; anything else keeps the fail-soft path.
	OwnerWait time.Duration
	// OwnerPoll is the pause between the poller's Redis reads (default 250 ms); the lease row is read at most once per OwnerLeaseEvery.
	OwnerPoll time.Duration
	// OwnerLeaseEvery is the least time between two reads of a group's lease row while waiting (default 1 s: the request pool is small).
	OwnerLeaseEvery time.Duration
	// Token returns this pod's fence token for the group, after an acquire (the lease store's view). The call that runs as the new owner carries
	// it in its context, so the owner guard and the permit see the new epoch. Nil: the context is unchanged.
	Token func(group string) (fence.Token, bool)
}

// Forwarder is the requester side: it decides whether a call that needs Rivian is served here or by the group's owner, and if by the
// owner pushes it, waits for the signed reply and verifies it. Nothing is acquired (C2): no owner means the call is served here.
type Forwarder struct {
	cfg   RequesterConfig
	mu    sync.Mutex
	polls map[string]*drainPoll // one poller per (group, old owner, epoch) on this pod: its waiters share the result
}

// NewForwarder validates cfg.
func NewForwarder(cfg RequesterConfig) (*Forwarder, error) {
	if cfg.Signer == nil || cfg.Redis == nil || cfg.Leases == nil {
		return nil, errors.New("forward: a requester needs a signer, a redis client and a lease view")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Jitter == nil {
		cfg.Jitter = func() time.Duration {
			return 200*time.Millisecond + time.Duration(rand.Int64N(int64(300*time.Millisecond)))
		}
	}
	return &Forwarder{cfg: cfg, polls: map[string]*drainPoll{}}, nil
}

// UnavailableResult is the 503 a handler answers with Retry-After RetryAfterSeconds: the reply of a call nobody could serve.
func UnavailableResult() Result { return Result{Status: StatusUnavailable, Class: ClassUnavailable} }

// Call is one operation that needs Rivian: the kind, the user and the group it is for, and the kind's args (a value that marshals to
// the kind's schema).
type Call struct {
	Kind  Kind
	User  string
	Group string
	Args  any
}

func (f *Forwarder) count(kind Kind, outcome string) {
	if f.cfg.Metrics != nil {
		f.cfg.Metrics.Total.WithLabelValues(string(kind), outcome).Inc()
	}
}

// NoGroup is for a call whose vehicle's group the handler cannot tell (it never reaches Do). It reports whether the handler may serve the
// call on this pod (counted `no_group`); with NoLocal it may not, and answers 503 Retry-After (counted `no_local`).
func (f *Forwarder) NoGroup(kind Kind) bool {
	if f.cfg.NoLocal {
		f.count(kind, "no_local")
		return false
	}
	f.count(kind, "no_group")
	return true
}

func (f *Forwarder) wait(k Kind) time.Duration {
	if d := f.cfg.Timeouts[k]; d > 0 {
		return d
	}
	return Kinds[k].Timeout
}

// Do runs the call and reports who served it. local is the code the handler runs today on a pod that holds the group; Do calls it when
// this pod holds the group, and as the fail-soft path when the call cannot be forwarded (outcome `local`), or the owner stays busy
// (`local_busy`).
//
// Order: held here (no Redis) -> lease row (system pool, cached) -> the owner's readiness marker -> the owner's queue length -> push,
// wait for the signed reply -> on `moved` or a timeout re-read the lease once and retry if the owner or epoch changed. With nobody new to ask,
// `moved` is served here (`local`: the owner has just said it is not the owner, which is the no-owner case); a timeout is a 503.
func (f *Forwarder) Do(ctx context.Context, c Call, local func(context.Context) (Result, error)) (Result, string, error) {
	start := time.Now()
	defer func() {
		if f.cfg.Metrics != nil {
			f.cfg.Metrics.Seconds.WithLabelValues(string(c.Kind)).Observe(time.Since(start).Seconds())
		}
	}()
	if _, ok := Kinds[c.Kind]; !ok {
		return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "rejected", errors.New("forward: not an allowlisted kind")
	}
	if epoch, ok := f.cfg.Leases.Held(c.Group); ok && !f.cfg.Leases.PastDeadline() && epoch > 0 {
		f.count(c.Kind, "held") // the normal case on the owner: not a fail-soft path (left out of the local share), but counted for volume
		if f.cfg.Token != nil {
			if tok, has := f.cfg.Token(c.Group); has {
				ctx = fence.With(ctx, tok) // the call is the group's own, so it carries the permit like an acquired one does
			}
		}
		res, err := local(ctx)
		return res, "held", err
	}
	owner, err := f.cfg.Leases.Owner(ctx, c.Group)
	if err != nil {
		f.cfg.Logger.Warn("forward: lease read failed", "err", err.Error())
		return f.fallback(ctx, c, "local", false, local)
	}
	if !owner.Found || owner.Pod == f.cfg.Pod || !ValidPod(owner.Pod) {
		// Only an absent or expired row (Found false) may be acquired; a live row that names this pod or an invalid pod may not.
		return f.fallback(ctx, c, "local", !owner.Found, local)
	}
	attempts := 0
	var sawBusy bool
	for {
		attempts++
		res, outcome := f.attempt(ctx, c, owner)
		switch outcome {
		case "ok", "error", "rejected":
			f.count(c.Kind, outcome)
			return res, outcome, nil
		case "unavailable":
			f.count(c.Kind, "unavailable")
			return res, "unavailable", nil
		case "badargs":
			// Our own arguments fail the kind's schema (a bug): nothing was sent. The call is served the way any fail-soft call is, and counted
			// `rejected` so it shows.
			return f.fallback(ctx, c, "rejected", false, local)
		case "redis":
			return f.fallback(ctx, c, "local", false, local) // the lease is live: not ready, or Redis is down
		case "draining":
			if f.cfg.OwnerWait <= 0 || !(f.cfg.NoLocal || f.cfg.AcquireOnRequest) {
				return f.fallback(ctx, c, "local", false, local)
			}
			next, found := f.waitForOwner(ctx, c, owner)
			if next.Pod == "" {
				return f.fallback(ctx, c, "local", !found, local) // out of time, or the row is gone and this pod may take it
			}
			owner = next
			continue
		case "busy":
			if sawBusy {
				return f.fallback(ctx, c, "local_busy", false, local)
			}
			sawBusy = true
			select {
			case <-ctx.Done():
				f.count(c.Kind, "timeout")
				return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "timeout", nil
			case <-time.After(f.cfg.Jitter()):
			}
			continue
		case "moved", "timeout":
			if attempts >= 2 || !Kinds[c.Kind].Retry {
				f.count(c.Kind, outcome)
				return Result{Status: StatusUnavailable, Class: ClassUnavailable}, outcome, nil
			}
			if lf, ok := f.cfg.Leases.(LeaseForgetter); ok {
				lf.Forget(c.Group)
			}
			again, err := f.cfg.Leases.Owner(ctx, c.Group)
			if err != nil || !again.Found || (again.Pod == owner.Pod && again.Epoch == owner.Epoch) || !ValidPod(again.Pod) {
				if outcome == "moved" {
					// The owner has just said it does not hold the group and the lease shows nobody else to ask: the "no owner" case, which M3
					// serves here (counted local). A timeout with nobody new stays a 503: that owner may still be inside a slow Rivian call.
					// With acquire-on-request the row is acquired only if it is absent or expired; a live row (the old owner, this pod) is a 503.
					return f.fallback(ctx, c, "local", err == nil && !again.Found, local)
				}
				f.count(c.Kind, outcome) // nobody new to ask
				return Result{Status: StatusUnavailable, Class: ClassUnavailable}, outcome, nil
			}
			owner = again
			continue
		}
	}
}

// fallback is what a call does when it cannot be forwarded and the lease says there is nobody to ask. M3: run it here (soft). With
// acquire-on-request (M4-2): acquire the group when its lease is absent or expired (mayAcquire), then run it as the owner (`acquired`);
// otherwise answer 503 (`unavailable`). A live lease is never taken.
func (f *Forwarder) fallback(ctx context.Context, c Call, softOutcome string, mayAcquire bool, local func(context.Context) (Result, error)) (Result, string, error) {
	if f.cfg.NoLocal {
		f.count(c.Kind, "no_local")
		return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "no_local", nil
	}
	if !f.cfg.AcquireOnRequest || f.cfg.Acquire == nil {
		return f.soft(ctx, c, softOutcome, local)
	}
	if mayAcquire {
		ok, err := f.cfg.Acquire(ctx, c.Group)
		if err != nil {
			f.cfg.Logger.Warn("forward: acquire on request failed", "err", err.Error())
		}
		if lf, isLF := f.cfg.Leases.(LeaseForgetter); isLF {
			lf.Forget(c.Group)
		}
		if ok {
			f.count(c.Kind, "acquired")
			// The call runs as the new owner: the new epoch's token is on its context.
			if f.cfg.Token != nil {
				if tok, has := f.cfg.Token(c.Group); has {
					ctx = fence.With(ctx, tok)
				}
			}
			res, lerr := local(ctx)
			return res, "acquired", lerr
		}
		// Lost the race: another requester took the primary key. Read the lease again and, if a live owner is there now, ask it once rather
		// than answering a burst of losers with 503s (design risk 3).
		if o, oerr := f.cfg.Leases.Owner(ctx, c.Group); oerr == nil && o.Found && o.Pod != f.cfg.Pod && ValidPod(o.Pod) {
			res, outcome := f.attempt(ctx, c, o)
			switch outcome {
			case "ok", "error", "rejected":
				f.count(c.Kind, outcome)
				return res, outcome, nil
			}
		}
	}
	f.count(c.Kind, "unavailable")
	return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "unavailable", nil
}

// soft is the fail-soft path: serve here (tokens are still on every pod until M4; M4 turns this into a 503).
func (f *Forwarder) soft(ctx context.Context, c Call, outcome string, local func(context.Context) (Result, error)) (Result, string, error) {
	f.count(c.Kind, outcome)
	res, err := local(ctx)
	return res, outcome, err
}

// attempt pushes one request to owner and waits. Its outcome is one of ok, error, rejected, unavailable, busy, moved, timeout, redis
// (Redis failed or the owner is not ready: the caller falls soft).
func (f *Forwarder) attempt(ctx context.Context, c Call, owner LeaseOwner) (Result, string) {
	rdb := f.cfg.Redis
	switch st, err := f.readiness(ctx, owner.Pod); {
	case err != nil:
		return Result{}, "redis" // Redis is down: serve here
	case st == markerDraining:
		return Result{}, "draining" // the owner has stopped taking work and still holds the lease: wait for its successor, nothing sent
	case st == markerAbsent:
		return Result{}, "redis" // not ready (an old image, a crashed loop, the flag off there): serve here
	}
	if n, err := rdb.LLen(ctx, f.cfg.Keys.Queue(owner.Pod)).Result(); err != nil {
		return Result{}, "redis"
	} else if n > MaxQueue {
		return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "unavailable"
	}
	now, err := rdb.Time(ctx).Result()
	if err != nil {
		return Result{}, "redis"
	}
	args, err := json.Marshal(c.Args)
	if err != nil {
		return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "rejected"
	}
	// The same test the owner applies, before anything is sent: a request the owner would refuse is never pushed, so the user does not wait
	// out the kind's timeout for an answer that cannot come.
	if _, verr := Kinds[c.Kind].Validate(args); verr != nil {
		f.cfg.Logger.Warn("forward: arguments refused before sending", "kind", string(c.Kind), "reason", verr.Error())
		return Result{}, "badargs"
	}
	id, err := NewID()
	if err != nil {
		return Result{}, "redis"
	}
	req := &Request{V: Version, ID: id, User: c.User, Group: c.Group, Epoch: owner.Epoch, Kind: c.Kind, Args: args,
		ExpiresMS: now.UnixMilli() + f.wait(c.Kind).Milliseconds()}
	f.cfg.Signer.SignRequest(req)
	body, err := json.Marshal(req)
	if err != nil || len(body) > MaxRequestBytes {
		return Result{Status: StatusUnavailable, Class: ClassUnavailable}, "rejected"
	}
	queue, replyKey, userSet := f.cfg.Keys.Queue(owner.Pod), f.cfg.Keys.Reply(id), f.cfg.Keys.UserSet(c.User)
	pipe := rdb.Pipeline()
	pipe.RPush(ctx, queue, body)
	pipe.Expire(ctx, queue, RequestListTTL*time.Second)
	pipe.SAdd(ctx, userSet, id)
	pipe.Expire(ctx, userSet, UserSetTTL*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		return Result{}, "redis"
	}
	cleanup := func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		pipe := rdb.Pipeline()
		pipe.SRem(cctx, userSet, id)
		pipe.Del(cctx, replyKey)
		_, _ = pipe.Exec(cctx)
	}
	defer cleanup()
	got, err := rdb.BLPop(ctx, f.wait(c.Kind)+time.Second, replyKey).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) || ctx.Err() == nil {
			return Result{}, "timeout"
		}
		return Result{}, "timeout"
	}
	if len(got) != 2 {
		return Result{}, "timeout"
	}
	rep, err := DecodeReply([]byte(got[1]))
	if err != nil || rep.ID != id || rep.User != c.User || f.cfg.Signer.VerifyReply(rep) != nil {
		f.cfg.Logger.Warn("forward: a reply was refused", "kind", string(c.Kind))
		return Result{Status: StatusBadReply, Class: ClassBadReply}, "rejected"
	}
	res := Result{Status: rep.Status, Class: rep.Class, Body: rep.Body}
	switch {
	case rep.Class == ClassMoved:
		return res, "moved"
	case rep.Class == ClassBusy:
		return res, "busy"
	case rep.Class == ClassBadRequest:
		return res, "badargs"
	case rep.Class == "timeout":
		return res, "timeout"
	case rep.Class == ClassInternal && rep.Status == StatusInternal:
		return res, "error"
	}
	return res, "ok"
}

// Nudge is the best-effort `adopt` push: user and group only, no reply is awaited, nothing is retried and nothing is served locally.
// It reports whether a message was pushed to the group's ready owner.
func (f *Forwarder) Nudge(ctx context.Context, user, group string) bool {
	return f.push(ctx, user, group, KindAdopt)
}

// ReceiptsSync is the same push for `receipts_sync`: the owner runs one summaries pass for the user (the Postgres claim caps it at one
// per 30 minutes), and a lost message falls back to the owner's sweep. It never calls Rivian here.
func (f *Forwarder) ReceiptsSync(ctx context.Context, user, group string) bool {
	return f.push(ctx, user, group, KindReceiptsSync)
}

// push puts a no-reply kind with empty args on the ready owner's queue.
func (f *Forwarder) push(ctx context.Context, user, group string, kind Kind) bool {
	owner, err := f.cfg.Leases.Owner(ctx, group)
	if err != nil || !owner.Found || !ValidPod(owner.Pod) || owner.Pod == f.cfg.Pod {
		return false
	}
	rdb := f.cfg.Redis
	if st, err := f.readiness(ctx, owner.Pod); err != nil || st != markerReady {
		return false
	}
	// A no-reply push never fills the owner's queue past what attempt() accepts: past MaxQueue every requester refuses this pod, so the
	// message is dropped (the sender's fallback is the owner's sweep).
	if n, err := rdb.LLen(ctx, f.cfg.Keys.Queue(owner.Pod)).Result(); err != nil || n >= MaxQueue {
		return false
	}
	now, err := rdb.Time(ctx).Result()
	if err != nil {
		return false
	}
	id, err := NewID()
	if err != nil {
		return false
	}
	req := &Request{V: Version, ID: id, User: user, Group: group, Epoch: owner.Epoch, Kind: kind, Args: json.RawMessage(`{}`),
		ExpiresMS: ExpiresMS(now.UnixMilli(), kind)}
	f.cfg.Signer.SignRequest(req)
	body, _ := json.Marshal(req)
	queue := f.cfg.Keys.Queue(owner.Pod)
	pipe := rdb.Pipeline()
	pipe.RPush(ctx, queue, body)
	pipe.Expire(ctx, queue, RequestListTTL*time.Second)
	_, err = pipe.Exec(ctx)
	return err == nil
}

// Readiness marker values: the owner writes markerReady while it takes work and markerDraining from SIGUSR1 until its lease is released.
const (
	markerReady    = "1"
	markerDraining = "draining"
	markerAbsent   = ""
)

// readiness reads a pod's marker: markerReady, markerDraining, or markerAbsent (never set, expired or deleted: not ready, or crashed).
func (f *Forwarder) readiness(ctx context.Context, pod string) (string, error) {
	v, err := f.cfg.Redis.Get(ctx, f.cfg.Keys.Ready(pod)).Result()
	if errors.Is(err, redis.Nil) {
		return markerAbsent, nil
	}
	if err != nil {
		return "", err
	}
	if v == markerDraining {
		return markerDraining, nil
	}
	return markerReady, nil // any other value is a marker an older image wrote: ready
}

// WaitBudget is how long a call of kind may wait for a draining owner, given what is left of the request's deadline (0: none is known) and
// the configured ceiling: the ceiling, less than what the deadline leaves after the kind's own timeout and 2 s of slack, and less than the
// kind's own cap (live_session: its 10 s poll must not stack). Never negative.
func WaitBudget(kind Kind, remaining, ceiling time.Duration) time.Duration {
	spec := Kinds[kind]
	wait := ceiling
	if spec.DrainWait > 0 && spec.DrainWait < wait {
		wait = spec.DrainWait
	}
	if remaining > 0 {
		if room := remaining - spec.Timeout - 2*time.Second; room < wait {
			wait = room
		}
	}
	if wait < 0 {
		return 0
	}
	return wait
}

// drainPoll is the shared wait for one draining owner: one goroutine watches, every waiter reads the result.
type drainPoll struct {
	done  chan struct{}
	owner LeaseOwner // set before done closes: the owner to ask now, zero Pod when there is none
	found bool       // the lease row still exists (false: absent or expired, so a pod that may acquire can)
}

// waitForOwner waits for the group's draining owner to be replaced by one that is ready to be asked, and returns it; a zero Pod means none
// in time (and found says whether the lease row is still there). The wait is capped by WaitBudget, ends when ctx does, and is over before
// anything is sent, so a call never runs twice for it.
func (f *Forwarder) waitForOwner(ctx context.Context, c Call, old LeaseOwner) (LeaseOwner, bool) {
	start := time.Now()
	var remaining time.Duration
	if dl, ok := ctx.Deadline(); ok {
		remaining = time.Until(dl)
		if remaining <= 0 {
			remaining = time.Nanosecond
		}
	}
	budget := WaitBudget(c.Kind, remaining, f.cfg.OwnerWait)
	if budget <= 0 {
		f.drainOutcome("timeout", start)
		return LeaseOwner{}, true
	}
	p := f.pollFor(c.Group, old)
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-p.done:
		if p.owner.Pod != "" {
			f.drainOutcome("forwarded", start)
		} else {
			f.drainOutcome("timeout", start)
		}
		return p.owner, p.found
	case <-ctx.Done():
		f.drainOutcome("canceled", start)
		return LeaseOwner{}, true
	case <-timer.C:
		f.drainOutcome("timeout", start)
		return LeaseOwner{}, true
	}
}

func (f *Forwarder) drainOutcome(outcome string, start time.Time) {
	if f.cfg.Metrics != nil {
		f.cfg.Metrics.DrainWait.WithLabelValues(outcome).Inc()
		f.cfg.Metrics.DrainWaitSeconds.Observe(time.Since(start).Seconds())
	}
}

// pollFor returns the poller for the draining owner, starting it for the first waiter.
func (f *Forwarder) pollFor(group string, old LeaseOwner) *drainPoll {
	key := group + "|" + old.Pod + "|" + strconv.FormatInt(old.Epoch, 10)
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.polls[key]; ok {
		return p
	}
	p := &drainPoll{done: make(chan struct{})}
	f.polls[key] = p
	go f.poll(key, group, old, p)
	return p
}

// poll watches until a ready owner is there, the old owner's marker is gone (it died: the old path), the old owner is ready again, or
// OwnerWait runs out. Redis is read every OwnerPoll; the lease row at most once per OwnerLeaseEvery (the request pool is small), and by this
// one goroutine for every waiter. It belongs to no request: its own deadline ends it.
func (f *Forwarder) poll(key, group string, old LeaseOwner, p *drainPoll) {
	defer func() {
		f.mu.Lock()
		delete(f.polls, key)
		f.mu.Unlock()
		close(p.done)
	}()
	pause, leaseEvery := f.cfg.OwnerPoll, f.cfg.OwnerLeaseEvery
	if pause <= 0 {
		pause = 250 * time.Millisecond
	}
	if leaseEvery <= 0 {
		leaseEvery = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), f.cfg.OwnerWait)
	defer cancel()
	p.found = true
	var lastLease time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		switch st, err := f.readiness(ctx, old.Pod); {
		case err != nil:
			continue
		case st == markerReady:
			p.owner = old // it is taking work again
			return
		case st == markerAbsent:
			return // it died (or finished): nobody to wait for, the caller takes the no-marker path
		}
		if time.Since(lastLease) < leaseEvery {
			continue
		}
		lastLease = time.Now()
		if lf, ok := f.cfg.Leases.(LeaseForgetter); ok {
			lf.Forget(group)
		}
		o, err := f.cfg.Leases.Owner(ctx, group)
		if err != nil {
			continue
		}
		p.found = o.Found
		switch {
		case !o.Found:
			if !f.cfg.NoLocal {
				return // a pod that may acquire the free row does so
			}
		case o.Pod == f.cfg.Pod || !ValidPod(o.Pod):
			return
		case o.Pod != old.Pod || o.Epoch != old.Epoch:
			if st, err := f.readiness(ctx, o.Pod); err == nil && st == markerReady {
				p.owner = o
				return
			}
		}
	}
}
