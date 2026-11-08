package forward

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
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
}

// Forwarder is the requester side: it decides whether a call that needs Rivian is served here or by the group's owner, and if by the
// owner pushes it, waits for the signed reply and verifies it. Nothing is acquired (C2): no owner means the call is served here.
type Forwarder struct{ cfg RequesterConfig }

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
	return &Forwarder{cfg: cfg}, nil
}

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

// NoGroup counts a call the handler served on this pod because it could not tell which group the vehicle belongs to (it never reaches Do).
func (f *Forwarder) NoGroup(kind Kind) { f.count(kind, "no_group") }

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
		case "redis":
			return f.fallback(ctx, c, "local", false, local) // the lease is live: not ready, or Redis is down
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
	if !f.cfg.AcquireOnRequest || f.cfg.Acquire == nil {
		return f.soft(ctx, c, softOutcome, local)
	}
	if mayAcquire {
		ok, err := f.cfg.Acquire(ctx, c.Group)
		if err != nil {
			f.cfg.Logger.Warn("forward: acquire on request failed", "err", err.Error())
		}
		if ok {
			if lf, isLF := f.cfg.Leases.(LeaseForgetter); isLF {
				lf.Forget(c.Group)
			}
			f.count(c.Kind, "acquired")
			res, lerr := local(ctx)
			return res, "acquired", lerr
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
	if n, err := rdb.Exists(ctx, f.cfg.Keys.Ready(owner.Pod)).Result(); err != nil || n == 0 {
		return Result{}, "redis" // not ready (an old image, a crashed loop, the flag off there) or Redis is down: serve here
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
	owner, err := f.cfg.Leases.Owner(ctx, group)
	if err != nil || !owner.Found || !ValidPod(owner.Pod) || owner.Pod == f.cfg.Pod {
		return false
	}
	rdb := f.cfg.Redis
	if n, err := rdb.Exists(ctx, f.cfg.Keys.Ready(owner.Pod)).Result(); err != nil || n == 0 {
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
	req := &Request{V: Version, ID: id, User: user, Group: group, Epoch: owner.Epoch, Kind: KindAdopt, Args: json.RawMessage(`{}`),
		ExpiresMS: ExpiresMS(now.UnixMilli(), KindAdopt)}
	f.cfg.Signer.SignRequest(req)
	body, _ := json.Marshal(req)
	queue := f.cfg.Keys.Queue(owner.Pod)
	pipe := rdb.Pipeline()
	pipe.RPush(ctx, queue, body)
	pipe.Expire(ctx, queue, RequestListTTL*time.Second)
	_, err = pipe.Exec(ctx)
	return err == nil
}
