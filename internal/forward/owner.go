package forward

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Statuses an Owner replies with when it does not run the kind. They are HTTP-ish numbers the requester maps with the handlers' own
// error mapping (the Class travels with them).
const (
	StatusMoved      = 421 // this pod no longer holds the group at that epoch: the requester re-reads the lease and retries once
	StatusBusy       = 503 // the user already has the maximum in flight
	StatusInternal   = 500
	StatusTooBig     = 502
	StatusBadRequest = 400 // a message our own key signed that fails the kind's schema: a bug on the sending side, answered at once
	ClassBadRequest  = "bad_request"
	ClassMoved       = "moved"
	ClassBusy        = "busy"
	ClassInternal    = "internal"
	ClassReplyTooBig = "reply_too_big"
	defaultWorkers   = 8
	defaultPerUser   = 2
	defaultBlockFor  = 2 * time.Second
)

// OwnerConfig wires an Owner. Redis is the forwarder's own client (a stuck BLPOP must not starve the rate limiter or the live-state
// store).
type OwnerConfig struct {
	Pod     string
	Keys    Keys
	Signer  *Signer
	Redis   redis.Cmdable
	Leases  LeaseView
	Runner  Runner
	Metrics *Metrics // nil: no metrics
	Logger  *slog.Logger

	Workers  int           // concurrent kinds on this pod (default 8)
	PerUser  int           // concurrent kinds for one user (default 2)
	BlockFor time.Duration // BLPOP timeout (default 2 s, so shutdown is prompt)
}

// Owner is the owner side: one dequeue loop on this pod's list, a bounded worker pool, and the readiness marker. It runs only the
// kinds in the allowlist, only for groups this pod holds at the request's epoch, and only before the request's Redis-clock expiry.
type Owner struct {
	cfg  OwnerConfig
	sem  chan struct{}
	mu   sync.Mutex
	user map[string]int
	wg   sync.WaitGroup
	// draining is set when this pod starts to drain (SIGUSR1): the readiness marker is dropped at once and not refreshed, so
	// requesters stop forwarding here, while the loop keeps answering what is already in its list.
	draining atomic.Bool
	notify   chan struct{}
}

// NewOwner validates cfg and fills the defaults.
func NewOwner(cfg OwnerConfig) (*Owner, error) {
	if cfg.Signer == nil || cfg.Redis == nil || cfg.Leases == nil || cfg.Runner == nil {
		return nil, errors.New("forward: an owner needs a signer, a redis client, a lease view and a runner")
	}
	if !ValidPod(cfg.Pod) {
		return nil, errors.New("forward: the pod id is empty or has characters a key cannot hold")
	}
	if cfg.Workers <= 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.PerUser <= 0 {
		cfg.PerUser = defaultPerUser
	}
	if cfg.BlockFor <= 0 {
		cfg.BlockFor = defaultBlockFor
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	return &Owner{cfg: cfg, sem: make(chan struct{}, cfg.Workers), user: map[string]int{}, notify: make(chan struct{}, 1)}, nil
}

func (o *Owner) count(kind Kind, outcome string) {
	if o.cfg.Metrics != nil {
		o.cfg.Metrics.Total.WithLabelValues(string(kind), outcome).Inc()
	}
}

// countRejected counts a message that could not be decoded, so it has no trustworthy kind: it goes to the fixed label "unknown",
// created at boot (an unbounded label would let garbage grow the series).
func (o *Owner) countRejected() {
	if o.cfg.Metrics != nil {
		o.cfg.Metrics.Total.WithLabelValues(KindUnknown, "rejected").Inc()
	}
}

// Run blocks until ctx is cancelled. It keeps the readiness marker fresh while it dequeues, and removes the marker on the way out so
// requesters stop forwarding here at once.
func (o *Owner) Run(ctx context.Context) {
	ready := o.cfg.Keys.Ready(o.cfg.Pod)
	queue := o.cfg.Keys.Queue(o.cfg.Pod)
	var markers sync.WaitGroup
	markers.Add(1)
	go func() {
		defer markers.Done()
		o.markReady(ctx, ready)
	}()
	defer func() {
		// Wait for the in-flight kinds, then drop the marker. A fresh context: ctx is already cancelled.
		o.wg.Wait()
		markers.Wait()
		dctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = o.cfg.Redis.Del(dctx, ready).Err()
		if o.cfg.Metrics != nil {
			o.cfg.Metrics.OwnerReady.Set(0)
		}
	}()
	for ctx.Err() == nil {
		o.observeDepth(ctx, queue)
		res, err := o.cfg.Redis.BLPop(ctx, o.cfg.BlockFor, queue).Result()
		switch {
		case errors.Is(err, redis.Nil):
			continue
		case err != nil:
			if ctx.Err() != nil {
				return
			}
			o.cfg.Logger.Warn("forward: dequeue failed", "err", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		if len(res) != 2 {
			continue
		}
		o.dispatch(ctx, []byte(res[1]))
	}
}

// SetDraining stops advertising this pod as ready (the marker is deleted now and not refreshed). In-flight and queued work finishes.
func (o *Owner) SetDraining() {
	if o.draining.Swap(true) {
		return
	}
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

func (o *Owner) markReady(ctx context.Context, key string) {
	set := func() {
		if o.draining.Load() {
			dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_ = o.cfg.Redis.Del(dctx, key).Err()
			if o.cfg.Metrics != nil {
				o.cfg.Metrics.OwnerReady.Set(0)
			}
			return
		}
		sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		ok := o.cfg.Redis.Set(sctx, key, "1", ReadyTTL*time.Second).Err() == nil
		if o.cfg.Metrics != nil {
			if ok {
				o.cfg.Metrics.OwnerReady.Set(1)
			} else {
				o.cfg.Metrics.OwnerReady.Set(0)
			}
		}
	}
	set()
	t := time.NewTicker(ReadyEvery * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			set()
		case <-o.notify:
			set()
		}
	}
}

func (o *Owner) observeDepth(ctx context.Context, queue string) {
	if o.cfg.Metrics == nil {
		return
	}
	if n, err := o.cfg.Redis.LLen(ctx, queue).Result(); err == nil {
		o.cfg.Metrics.QueueDepth.Set(float64(n))
	}
}

// dispatch admits one dequeued element: the signature first, then everything else. A run that passes is handed to the worker pool
// (blocking here when the pool is full, which is the backpressure: the list absorbs the queue and requesters refuse past MaxQueue).
func (o *Owner) dispatch(ctx context.Context, raw []byte) {
	req, err := DecodeRequest(raw)
	if err != nil {
		o.countRejected()
		return
	}
	args, err := Check(o.cfg.Signer, req)
	if err != nil {
		o.count(kindOrUnknown(req.Kind), "rejected")
		if !errors.Is(err, ErrBadSig) {
			// The signature held, so the sender is one of our servers and the arguments are a bug of ours: say so now instead of leaving the
			// requester to wait out its timeout for an answer that will never come. A bad signature stays silent.
			o.cfg.Logger.Warn("forward: a signed request failed its schema", "kind", string(kindOrUnknown(req.Kind)), "reason", err.Error())
			o.reply(ctx, req, Reply{OK: false, Status: StatusBadRequest, Class: ClassBadRequest})
		}
		return
	}
	now, err := o.redisTimeMS(ctx)
	if err != nil {
		o.cfg.Logger.Warn("forward: redis time failed", "err", err.Error())
		return
	}
	if Expired(now, req.ExpiresMS) {
		o.count(req.Kind, "timeout") // the requester has given up: nothing to run and nobody to answer
		return
	}
	if !o.holds(ctx, req) {
		o.count(req.Kind, "moved")
		o.reply(ctx, req, Reply{OK: false, Status: StatusMoved, Class: ClassMoved})
		return
	}
	if !o.admitUser(req.User) {
		o.count(req.Kind, "unavailable")
		o.reply(ctx, req, Reply{OK: false, Status: StatusBusy, Class: ClassBusy})
		return
	}
	select {
	case o.sem <- struct{}{}:
	case <-ctx.Done():
		o.releaseUser(req.User)
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer func() { <-o.sem }()
		defer o.releaseUser(req.User)
		o.run(ctx, req, args, now)
	}()
}

// holds: this pod holds the group at exactly the request's epoch, has not passed its local deadline, and the database still shows
// this pod as the group's owner at that epoch (a deleted user's lease row is gone: the request is inert).
func (o *Owner) holds(ctx context.Context, req *Request) bool {
	epoch, ok := o.cfg.Leases.Held(req.Group)
	if !ok || epoch != req.Epoch || o.cfg.Leases.PastDeadline() {
		return false
	}
	row, err := o.cfg.Leases.Owner(ctx, req.Group)
	return err == nil && row.Found && row.Pod == o.cfg.Pod && row.Epoch == req.Epoch
}

func (o *Owner) admitUser(user string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.user[user] >= o.cfg.PerUser {
		return false
	}
	o.user[user]++
	return true
}

func (o *Owner) releaseUser(user string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.user[user] <= 1 {
		delete(o.user, user)
		return
	}
	o.user[user]--
}

func (o *Owner) run(ctx context.Context, req *Request, args any, nowMS int64) {
	timeout := Kinds[req.Kind].Timeout
	if left := time.Duration(req.ExpiresMS-nowMS) * time.Millisecond; left < timeout {
		timeout = left
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := o.cfg.Runner.Run(rctx, req.Kind, req.User, req.Group, args)
	switch {
	case err != nil && rctx.Err() != nil:
		o.count(req.Kind, "timeout")
		o.reply(ctx, req, Reply{OK: false, Status: 504, Class: "timeout"})
		return
	case err != nil:
		o.count(req.Kind, "error")
		o.reply(ctx, req, Reply{OK: false, Status: StatusInternal, Class: ClassInternal})
		return
	}
	body := res.Body
	if len(body) > MaxReplyBytes-1024 {
		// Counted before the answer is sent, like every other outcome: once the requester has the reply the count must already be there (a reader
		// of the metric, or a test, that sees the reply must also see the count).
		o.count(req.Kind, "too_big")
		o.reply(ctx, req, Reply{OK: false, Status: StatusTooBig, Class: ClassReplyTooBig})
		return
	}
	o.count(req.Kind, "ok")
	o.reply(ctx, req, Reply{OK: res.Status >= 200 && res.Status < 300, Status: res.Status, Class: res.Class, Body: body})
}

// reply signs and pushes the answer to the one-shot reply list. adopt has no waiting requester, so it writes none.
func (o *Owner) reply(ctx context.Context, req *Request, r Reply) {
	if Kinds[req.Kind].NoReply {
		return
	}
	r.V, r.ID, r.User = Version, req.ID, req.User
	o.cfg.Signer.SignReply(&r)
	b, err := json.Marshal(&r)
	if err != nil {
		return
	}
	key := o.cfg.Keys.Reply(req.ID) // req.ID passed ValidID in DecodeRequest: a request cannot name a key
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	pipe := o.cfg.Redis.Pipeline()
	pipe.RPush(pctx, key, b)
	pipe.Expire(pctx, key, ReplyTTL*time.Second)
	if _, err := pipe.Exec(pctx); err != nil {
		o.cfg.Logger.Warn("forward: reply failed", "kind", string(req.Kind), "err", err.Error())
	}
}

func (o *Owner) redisTimeMS(ctx context.Context) (int64, error) {
	t, err := o.cfg.Redis.Time(ctx).Result()
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}

func kindOrUnknown(k Kind) Kind {
	if _, ok := Kinds[k]; ok {
		return k
	}
	return Kind(KindUnknown)
}

// KindUnknown is the metric label for a message whose kind is not in the allowlist (or that could not be decoded). It is created at
// boot so a flood of garbage shows as rejected{kind="unknown"} without an unbounded label.
const KindUnknown = "unknown"
