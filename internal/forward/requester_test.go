package forward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/fence"
	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/forward/forwardtest"
)

// A small fleet on one miniredis: owner pods A and B (each with its own lease view, runner and Owner loop), and a requester pod C whose
// view of the leases the test controls.
type fleet struct {
	t       *testing.T
	mr      *miniredis.Miniredis
	rdb     *redis.Client
	signer  *forward.Signer
	keys    forward.Keys
	m       *forward.Metrics
	runners map[string]*forwardtest.Runner
	leases  map[string]*forwardtest.LeaseView
	stops   []context.CancelFunc
	view    *switchView // the requester's lease view
}

// switchView is the requester's lease view: it answers with owner, and a Forget (the requester re-reading after a moved reply or a
// timeout) flips it to next if one is set.
type switchView struct {
	forwardtest.LeaseView
	next *forward.LeaseOwner
}

func (v *switchView) Forget(group string) {
	if v.next != nil {
		v.SetOwner(group, *v.next)
		v.next = nil
	}
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = rdb.Close() })
	signer, _ := forward.NewSigner(bytes.Repeat([]byte{7}, forward.KeyLen))
	f := &fleet{t: t, mr: mr, rdb: rdb, signer: signer, m: forward.NewMetrics(prometheus.NewRegistry()),
		runners: map[string]*forwardtest.Runner{}, leases: map[string]*forwardtest.LeaseView{}, view: &switchView{}}
	f.view.Owners = map[string]forward.LeaseOwner{group: {Found: true, Pod: "pod-a", Epoch: 7}}
	t.Cleanup(func() {
		for _, s := range f.stops {
			s()
		}
	})
	return f
}

// owner starts an Owner loop for pod holding the group at epoch.
func (f *fleet) owner(pod string, epoch int64, held bool, mut func(*forward.OwnerConfig)) {
	f.t.Helper()
	lv := &forwardtest.LeaseView{Owners: map[string]forward.LeaseOwner{group: {Found: true, Pod: pod, Epoch: epoch}}}
	if held {
		lv.HeldAt = map[string]int64{group: epoch}
	}
	rn := &forwardtest.Runner{Reply: forward.Result{Status: 200, Body: json.RawMessage(`{"served_by":"` + pod + `"}`)}}
	f.leases[pod], f.runners[pod] = lv, rn
	cfg := forward.OwnerConfig{Pod: pod, Keys: f.keys, Signer: f.signer, Redis: f.rdb, Leases: lv, Runner: rn, BlockFor: time.Second}
	if mut != nil {
		mut(&cfg)
	}
	o, err := forward.NewOwner(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.stops = append(f.stops, cancel)
	go o.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for !f.mr.Exists(f.keys.Ready(pod)) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fleet) requester(mut func(*forward.RequesterConfig)) *forward.Forwarder {
	f.t.Helper()
	cfg := forward.RequesterConfig{Pod: "pod-c", Keys: f.keys, Signer: f.signer, Redis: f.rdb, Leases: f.view, Metrics: f.m,
		Jitter: func() time.Duration { return 10 * time.Millisecond }}
	if mut != nil {
		mut(&cfg)
	}
	fw, err := forward.NewForwarder(cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	return fw
}

func (f *fleet) out(kind forward.Kind, outcome string) float64 {
	return testutil.ToFloat64(f.m.Total.WithLabelValues(string(kind), outcome))
}

var callFresh = forward.Call{Kind: forward.KindFreshState, User: user, Group: group, Args: forward.FreshStateArgs{VehicleID: "v-1", MaxAgeMS: 30000}}

type localCounter struct{ n atomic.Int32 }

func (l *localCounter) fn(context.Context) (forward.Result, error) {
	l.n.Add(1)
	return forward.Result{Status: 200, Body: json.RawMessage(`{"served_by":"local"}`)}, nil
}

// T8: a call on a pod that is not the owner is served by the owner: its runner runs it once, the requester's own code never runs.
func TestRequesterForwardsToTheOwner(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	var local localCounter
	res, outcome, err := f.requester(nil).Do(context.Background(), callFresh, local.fn)
	if err != nil || outcome != "ok" || res.Status != 200 || string(res.Body) != `{"served_by":"pod-a"}` {
		t.Fatalf("res=%+v outcome=%q err=%v", res, outcome, err)
	}
	if local.n.Load() != 0 || len(f.runners["pod-a"].Calls()) != 1 {
		t.Errorf("local ran %d times, owner ran %d times; want 0 and 1", local.n.Load(), len(f.runners["pod-a"].Calls()))
	}
	if f.out(forward.KindFreshState, "ok") != 1 {
		t.Error("ok counted")
	}
	// Nothing of the exchange is left in Redis: the reply key is read and gone, the user's set is emptied.
	if f.mr.Exists(f.keys.Reply("*")) {
		t.Error("a reply key lingers")
	}
	if m, _ := f.rdb.SMembers(context.Background(), f.keys.UserSet(user)).Result(); len(m) != 0 {
		t.Errorf("the user's in-flight set still holds %v", m)
	}
	if keys, _ := f.rdb.Keys(context.Background(), f.keys.Reply("*")).Result(); len(keys) != 0 {
		t.Errorf("reply keys left: %v", keys)
	}
}

// An error class survives the whole trip (T7): the requester returns the owner's status and class as they were.
func TestRequesterReturnsTheOwnersStatusAndClass(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	f.runners["pod-a"].Reply = forward.Result{Status: 429, Class: "quota", Body: json.RawMessage(`{"retry_after":30}`)}
	res, outcome, _ := f.requester(nil).Do(context.Background(), callFresh, (&localCounter{}).fn)
	if outcome != "ok" || res.Status != 429 || res.Class != "quota" || string(res.Body) != `{"retry_after":30}` {
		t.Fatalf("res=%+v outcome=%q", res, outcome)
	}
}

// The owner itself runs the call with no Redis at all.
func TestRequesterOnTheOwnerRunsLocallyWithoutRedis(t *testing.T) {
	f := newFleet(t)
	f.view.HeldAt = map[string]int64{group: 7}
	before := f.mr.CommandCount()
	var local localCounter
	_, outcome, err := f.requester(nil).Do(context.Background(), callFresh, local.fn)
	if err != nil || outcome != "held" || local.n.Load() != 1 {
		t.Fatalf("outcome=%q err=%v local=%d", outcome, err, local.n.Load())
	}
	if f.mr.CommandCount() != before {
		t.Errorf("the owner's own call used Redis (%d commands)", f.mr.CommandCount()-before)
	}
	// A held-here call increments `held` and nothing else: it is not a fail-soft path.
	if f.out(forward.KindFreshState, "held") != 1 {
		t.Error("held counted once")
	}
	for _, o := range forward.Outcomes {
		if o != "held" && f.out(forward.KindFreshState, o) != 0 {
			t.Errorf("a held-here call must not count %q", o)
		}
	}
	// Past its local deadline it does not trust its lease: it forwards (here: nobody, so it falls soft).
	f.view.SetPast(true)
	f.view.HeldAt = map[string]int64{group: 7}
	f.view.SetOwner(group, forward.LeaseOwner{})
	if _, outcome, _ := f.requester(nil).Do(context.Background(), callFresh, local.fn); outcome != "local" {
		t.Errorf("past the deadline: outcome=%q, want the fail-soft local", outcome)
	}
}

// T11: no owner, owner not ready, Redis down: served locally, counted `local`. Never an acquire.
func TestRequesterFailsSoft(t *testing.T) {
	for name, setup := range map[string]func(*fleet){
		"no owner":               func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{}) },
		"owner not ready":        func(f *fleet) { /* the lease row names pod-a, which runs no loop */ },
		"redis down":             func(f *fleet) { f.owner("pod-a", 7, true, nil); f.mr.Close() },
		"lease read fails":       func(f *fleet) { f.view.OwnerErr = errors.New("db down") },
		"the row names this pod": func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{Found: true, Pod: "pod-c", Epoch: 7}) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleet(t)
			setup(f)
			var local localCounter
			res, outcome, err := f.requester(nil).Do(context.Background(), callFresh, local.fn)
			if err != nil || outcome != "local" || local.n.Load() != 1 || res.Status != 200 {
				t.Fatalf("outcome=%q local=%d res=%+v err=%v", outcome, local.n.Load(), res, err)
			}
			if f.out(forward.KindFreshState, "local") != 1 {
				t.Error("local counted")
			}
		})
	}
}

// T15: a full queue is refused with a 503 whatever the pod; nothing is pushed.
func TestRequesterRefusesAFullQueue(t *testing.T) {
	f := newFleet(t)
	_ = f.mr.Set(f.keys.Ready("pod-a"), "1") // ready, but no loop is draining it
	for i := 0; i < forward.MaxQueue+1; i++ {
		_, _ = f.mr.Push(f.keys.Queue("pod-a"), "x")
	}
	var local localCounter
	res, outcome, _ := f.requester(nil).Do(context.Background(), callFresh, local.fn)
	if outcome != "unavailable" || res.Status != forward.StatusUnavailable || local.n.Load() != 0 {
		t.Fatalf("outcome=%q res=%+v local=%d", outcome, res, local.n.Load())
	}
	if n, _ := f.rdb.LLen(context.Background(), f.keys.Queue("pod-a")).Result(); n != forward.MaxQueue+1 {
		t.Errorf("nothing may be pushed to a full queue, length is %d", n)
	}
}

// T10 (moved): the first owner answers `moved` (it lost the group); the lease is re-read, the new owner is asked once, and it serves.
func TestRequesterRetriesOnMovedWhenTheOwnerChanged(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, false, nil) // pod-a no longer holds the group
	f.owner("pod-b", 9, true, nil)
	f.view.next = &forward.LeaseOwner{Found: true, Pod: "pod-b", Epoch: 9}
	var local localCounter
	res, outcome, err := f.requester(nil).Do(context.Background(), callFresh, local.fn)
	if err != nil || outcome != "ok" || string(res.Body) != `{"served_by":"pod-b"}` {
		t.Fatalf("outcome=%q res=%+v err=%v", outcome, res, err)
	}
	if len(f.runners["pod-a"].Calls()) != 0 || len(f.runners["pod-b"].Calls()) != 1 || local.n.Load() != 0 {
		t.Errorf("runs: a=%d b=%d local=%d; want 0, 1, 0 (exactly one run, on the new owner)", len(f.runners["pod-a"].Calls()), len(f.runners["pod-b"].Calls()), local.n.Load())
	}
}

// moved with nobody new to ask (the lease still names the old owner, the row is gone, or it names this pod): the owner has just said it does
// not hold the group and the lease shows no one else, which is the "no owner" case, served here and counted local. The old owner ran nothing;
// the call runs once, here.
func TestRequesterMovedWithNoNewOwnerIsServedLocally(t *testing.T) {
	for name, prep := range map[string]func(f *fleet){
		"unchanged": func(*fleet) {},
		"row gone":  func(f *fleet) { f.view.next = nil; f.view.DeleteOwner(group) },
		"this pod":  func(f *fleet) { f.view.next = &forward.LeaseOwner{Found: true, Pod: "pod-c", Epoch: 9} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleet(t)
			f.owner("pod-a", 7, false, nil)
			prep(f)
			var local localCounter
			res, outcome, err := f.requester(nil).Do(context.Background(), callFresh, local.fn)
			if err != nil || outcome != "local" || res.Status == forward.StatusUnavailable {
				t.Fatalf("outcome=%q res=%+v err=%v, want a local run", outcome, res, err)
			}
			if local.n.Load() != 1 || len(f.runners["pod-a"].Calls()) != 0 {
				t.Errorf("local runs %d, owner runs %d; want 1 and 0", local.n.Load(), len(f.runners["pod-a"].Calls()))
			}
			if f.out(forward.KindFreshState, "local") != 1 || f.out(forward.KindFreshState, "moved") != 0 {
				t.Errorf("local=%v moved=%v, want 1 and 0", f.out(forward.KindFreshState, "local"), f.out(forward.KindFreshState, "moved"))
			}
		})
	}
}

// moved twice (the new owner also says moved) is still a 503: two owners in a row disagree with the lease, and the call is not run anywhere.
func TestRequesterMovedTwiceIs503(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, false, nil)
	f.owner("pod-b", 9, false, nil)
	f.view.next = &forward.LeaseOwner{Found: true, Pod: "pod-b", Epoch: 9}
	var local localCounter
	res, outcome, _ := f.requester(nil).Do(context.Background(), callFresh, local.fn)
	if outcome != "moved" || res.Status != forward.StatusUnavailable || local.n.Load() != 0 {
		t.Fatalf("outcome=%q res=%+v local=%d", outcome, res, local.n.Load())
	}
}

// A timeout (the owner is ready but never answers): the lease is re-read; with no new owner the answer is a 503, and nothing is left
// behind that names a reply key.
func TestRequesterTimesOutCleanly(t *testing.T) {
	f := newFleet(t)
	_ = f.mr.Set(f.keys.Ready("pod-a"), "1")
	req := f.requester(func(c *forward.RequesterConfig) {
		c.Timeouts = map[forward.Kind]time.Duration{forward.KindFreshState: 300 * time.Millisecond}
	})
	start := time.Now()
	res, outcome, _ := req.Do(context.Background(), callFresh, (&localCounter{}).fn)
	if outcome != "timeout" || res.Status != forward.StatusUnavailable {
		t.Fatalf("outcome=%q res=%+v", outcome, res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a call must end within its kind timeout plus slack, took %v", d)
	}
	if m, _ := f.rdb.SMembers(context.Background(), f.keys.UserSet(user)).Result(); len(m) != 0 {
		t.Errorf("the user's set still holds %v after a timeout", m)
	}
}

// The user's concurrency cap: still busy after the one jittered retry is served on this pod as `local_busy`.
func TestRequesterBusyThenLocalBusy(t *testing.T) {
	release := make(chan struct{})
	f := newFleet(t)
	lv := &forwardtest.LeaseView{Owners: map[string]forward.LeaseOwner{group: {Found: true, Pod: "pod-a", Epoch: 7}}, HeldAt: map[string]int64{group: 7}}
	o, err := forward.NewOwner(forward.OwnerConfig{Pod: "pod-a", Keys: f.keys, Signer: f.signer, Redis: f.rdb, Leases: lv, Runner: &blockingRunner{release: release, started: func() {}}, BlockFor: time.Second, PerUser: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go o.Run(ctx)
	for !f.mr.Exists(f.keys.Ready("pod-a")) {
		time.Sleep(5 * time.Millisecond)
	}
	req := f.requester(nil)
	first := make(chan string, 1)
	go func() { _, o, _ := req.Do(context.Background(), callFresh, (&localCounter{}).fn); first <- o }()
	time.Sleep(150 * time.Millisecond) // the first call is in flight, holding the user's slot
	var local localCounter
	_, outcome, _ := req.Do(context.Background(), callFresh, local.fn)
	if outcome != "local_busy" || local.n.Load() != 1 {
		t.Fatalf("outcome=%q local=%d, want local_busy served here", outcome, local.n.Load())
	}
	if f.out(forward.KindFreshState, "local_busy") != 1 {
		t.Error("local_busy counted")
	}
	close(release)
	if o := <-first; o != "ok" {
		t.Errorf("the first call: %q, want ok", o)
	}
}

// T6/T12: a reply that is unsigned, signed by another key, or for another user is refused: no result is taken from it.
func TestRequesterRefusesABadReply(t *testing.T) {
	for name, forge := range map[string]func(f *fleet, req *forward.Request) []byte{
		"unsigned": func(f *fleet, req *forward.Request) []byte {
			b, _ := json.Marshal(&forward.Reply{V: forward.Version, ID: req.ID, User: req.User, OK: true, Status: 200, Body: json.RawMessage(`{"pwned":1}`)})
			return b
		},
		"another key": func(f *fleet, req *forward.Request) []byte {
			other, _ := forward.NewSigner(bytes.Repeat([]byte{9}, forward.KeyLen))
			r := &forward.Reply{V: forward.Version, ID: req.ID, User: req.User, OK: true, Status: 200, Body: json.RawMessage(`{"pwned":1}`)}
			other.SignReply(r)
			b, _ := json.Marshal(r)
			return b
		},
		"another user": func(f *fleet, req *forward.Request) []byte {
			r := &forward.Reply{V: forward.Version, ID: req.ID, User: "someone-else", OK: true, Status: 200, Body: json.RawMessage(`{"pwned":1}`)}
			f.signer.SignReply(r)
			b, _ := json.Marshal(r)
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleet(t)
			_ = f.mr.Set(f.keys.Ready("pod-a"), "1")
			// A fake owner: take the request, push a forged reply.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				for ctx.Err() == nil {
					got, err := f.rdb.BLPop(ctx, time.Second, f.keys.Queue("pod-a")).Result()
					if err != nil || len(got) != 2 {
						continue
					}
					req, derr := forward.DecodeRequest([]byte(got[1]))
					if derr != nil {
						continue
					}
					f.rdb.RPush(ctx, f.keys.Reply(req.ID), forge(f, req))
				}
			}()
			var local localCounter
			res, outcome, _ := f.requester(nil).Do(context.Background(), callFresh, local.fn)
			if outcome != "rejected" || res.Status != forward.StatusBadReply || res.Class != forward.ClassBadReply || len(res.Body) != 0 {
				t.Fatalf("outcome=%q res=%+v", outcome, res)
			}
			if local.n.Load() != 0 {
				t.Error("a refused reply must not fall back to serving here")
			}
		})
	}
}

// A kind outside the allowlist is refused without touching Redis.
func TestRequesterRefusesAnUnknownKind(t *testing.T) {
	f := newFleet(t)
	before := f.mr.CommandCount()
	_, outcome, err := f.requester(nil).Do(context.Background(), forward.Call{Kind: "exec", User: user, Group: group, Args: struct{}{}}, (&localCounter{}).fn)
	if err == nil || outcome != "rejected" || f.mr.CommandCount() != before {
		t.Errorf("outcome=%q err=%v, commands used %d", outcome, err, f.mr.CommandCount()-before)
	}
}

// adopt is a best-effort nudge: pushed to a ready owner, no wait, no reply, nothing local; not pushed when the owner is not ready.
func TestRequesterNudge(t *testing.T) {
	f := newFleet(t)
	req := f.requester(nil)
	if req.Nudge(context.Background(), user, group) {
		t.Error("nothing is pushed to an owner that is not ready")
	}
	f.owner("pod-a", 7, true, nil)
	if !req.Nudge(context.Background(), user, group) {
		t.Fatal("a ready owner gets the nudge")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(f.runners["pod-a"].Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c := f.runners["pod-a"].Calls(); len(c) != 1 || c[0].Kind != forward.KindAdopt || c[0].User != user {
		t.Fatalf("owner calls = %+v", c)
	}
}

// receipts_sync is the same kind of push, but its own kind: the owner's runner sees receipts_sync, never adopt (which an old receiver would
// run as owner discovery), and an owner that is not ready gets nothing.
func TestRequesterReceiptsSyncPush(t *testing.T) {
	f := newFleet(t)
	req := f.requester(nil)
	if req.ReceiptsSync(context.Background(), user, group) {
		t.Error("nothing is pushed to an owner that is not ready")
	}
	f.owner("pod-a", 7, true, nil)
	if !req.ReceiptsSync(context.Background(), user, group) {
		t.Fatal("a ready owner gets the push")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(f.runners["pod-a"].Calls()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c := f.runners["pod-a"].Calls(); len(c) != 1 || c[0].Kind != forward.KindReceiptsSync || c[0].User != user {
		t.Fatalf("owner calls = %+v", c)
	}
}

// A no-reply push is dropped once the owner's queue is at MaxQueue, so it can never be the thing that makes every requester refuse that pod.
func TestRequesterPushDropsAtMaxQueue(t *testing.T) {
	f := newFleet(t)
	req := f.requester(nil)
	f.owner("pod-a", 7, true, nil)
	f.stops[len(f.stops)-1]() // stop the owner's loop so it does not drain the queue; its ready key stays
	time.Sleep(100 * time.Millisecond)
	q := f.keys.Queue("pod-a")
	for i := 0; i < forward.MaxQueue+5; i++ { // a few extra: the stopping loop may take one
		f.rdb.RPush(context.Background(), q, "x")
	}
	before := f.rdb.LLen(context.Background(), q).Val()
	if req.ReceiptsSync(context.Background(), user, group) {
		t.Error("a push to an owner whose queue is full must be dropped")
	}
	if n := f.rdb.LLen(context.Background(), q).Val(); n != before {
		t.Errorf("queue length %d, want %d", n, before)
	}
}

func TestNewForwarderValidates(t *testing.T) {
	f := newFleet(t)
	for name, mut := range map[string]func(*forward.RequesterConfig){
		"no signer": func(c *forward.RequesterConfig) { c.Signer = nil }, "no redis": func(c *forward.RequesterConfig) { c.Redis = nil },
		"no leases": func(c *forward.RequesterConfig) { c.Leases = nil },
	} {
		cfg := forward.RequesterConfig{Signer: f.signer, Redis: f.rdb, Leases: f.view}
		mut(&cfg)
		if _, err := forward.NewForwarder(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// A call a handler serves on this pod because it cannot tell the vehicle's group is counted as no_group, so the local share sees it.
func TestNoGroupIsCounted(t *testing.T) {
	f := newFleet(t)
	fw := f.requester(nil)
	fw.NoGroup(forward.KindFreshState)
	if f.out(forward.KindFreshState, "no_group") != 1 || f.out(forward.KindFreshState, "local") != 0 {
		t.Errorf("no_group=%v local=%v, want 1 and 0", f.out(forward.KindFreshState, "no_group"), f.out(forward.KindFreshState, "local"))
	}
}

// M4-2, acquire-on-request. The only trigger for an acquire is a lease row absent or expired by the database clock (Found false). A live
// lease (owner not ready, Redis down, busy, moved to someone or to nobody the row can name) is a 503 and the acquire is never asked.
func TestAcquireOnRequestOnlyForAnAbsentOrExpiredLease(t *testing.T) {
	type tc struct {
		setup        func(f *fleet)
		wantAcquire  bool   // Acquire is asked
		acquireOK    bool   // and succeeds
		wantOutcome  string // what Do reports
		wantLocalRun int32
	}
	for name, c := range map[string]tc{
		"no owner (absent or expired) and the acquire works": {func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{}) }, true, true, "acquired", 1},
		"no owner and the acquire fails":                     {func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{}) }, true, false, "unavailable", 0},
		"owner not ready, lease live":                        {func(f *fleet) {}, false, false, "unavailable", 0},
		"redis down, lease live":                             {func(f *fleet) { f.owner("pod-a", 7, true, nil); f.mr.Close() }, false, false, "unavailable", 0},
		"lease read fails":                                   {func(f *fleet) { f.view.OwnerErr = errors.New("db down") }, false, false, "unavailable", 0},
		"the live row names this pod":                        {func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{Found: true, Pod: "pod-c", Epoch: 7}) }, false, false, "unavailable", 0},
		"moved, row unchanged (live)":                        {func(f *fleet) { f.owner("pod-a", 7, false, nil) }, false, false, "unavailable", 0},
		"moved, row gone":                                    {func(f *fleet) { f.owner("pod-a", 7, false, nil); f.view.DeleteOwner(group) }, true, true, "acquired", 1},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFleet(t)
			c.setup(f)
			var asked atomic.Int32
			var local localCounter
			fw := f.requester(func(cfg *forward.RequesterConfig) {
				cfg.AcquireOnRequest = true
				cfg.Acquire = func(context.Context, string) (bool, error) { asked.Add(1); return c.acquireOK, nil }
			})
			res, outcome, err := fw.Do(context.Background(), callFresh, local.fn)
			if err != nil || outcome != c.wantOutcome || local.n.Load() != c.wantLocalRun {
				t.Fatalf("outcome=%q local=%d res=%+v err=%v, want %q and %d local runs", outcome, local.n.Load(), res, err, c.wantOutcome, c.wantLocalRun)
			}
			if (asked.Load() > 0) != c.wantAcquire {
				t.Errorf("acquire asked %d times, want asked=%v", asked.Load(), c.wantAcquire)
			}
			if c.wantOutcome == "unavailable" && res.Status != forward.StatusUnavailable {
				t.Errorf("status %d, want 503", res.Status)
			}
			if f.out(forward.KindFreshState, c.wantOutcome) != 1 || f.out(forward.KindFreshState, "local") != 0 || f.out(forward.KindFreshState, "local_busy") != 0 {
				t.Errorf("counted %s=%v local=%v: want exactly the one outcome", c.wantOutcome, f.out(forward.KindFreshState, c.wantOutcome), f.out(forward.KindFreshState, "local"))
			}
		})
	}
}

// Busy twice is owner saturation: a 503 with acquire-on-request, never an acquire and never a local call.
func TestAcquireOnRequestBusyIs503(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	// Fill the user's cap at the owner so the next call is told busy twice.
	release := make(chan struct{})
	f.runners["pod-a"].Gate = release
	fw := f.requester(func(cfg *forward.RequesterConfig) {
		cfg.AcquireOnRequest = true
		cfg.Acquire = func(context.Context, string) (bool, error) {
			t.Error("acquire asked for a busy owner")
			return true, nil
		}
	})
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = fw.Do(context.Background(), callFresh, (&localCounter{}).fn) }()
	}
	for dl := time.Now().Add(3 * time.Second); len(f.runners["pod-a"].Calls()) < 2 && time.Now().Before(dl); {
		time.Sleep(5 * time.Millisecond)
	}
	var local localCounter
	res, outcome, _ := fw.Do(context.Background(), callFresh, local.fn)
	close(release)
	wg.Wait()
	if outcome != "unavailable" || res.Status != forward.StatusUnavailable || local.n.Load() != 0 {
		t.Fatalf("outcome=%q res=%+v local=%d, want a 503 and no local call", outcome, res, local.n.Load())
	}
}

// Two requesters race for an ownerless group: one wins the primary key and runs the call as the owner (`acquired`); the loser re-reads the
// lease, finds the live owner and forwards once (`ok`). No 503s for the losers of a burst (design risk 3).
func TestAcquireRaceLoserForwardsToTheWinner(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 9, true, nil) // the winner of the race, once it holds the row
	f.view.SetOwner(group, forward.LeaseOwner{})
	var calls atomic.Int32
	fw := f.requester(func(cfg *forward.RequesterConfig) {
		cfg.AcquireOnRequest = true
		cfg.Acquire = func(context.Context, string) (bool, error) {
			if calls.Add(1) == 1 {
				return true, nil // the winner
			}
			f.view.SetOwner(group, forward.LeaseOwner{Found: true, Pod: "pod-a", Epoch: 9}) // the loser sees who won
			return false, nil
		}
	})
	var local localCounter
	var wg sync.WaitGroup
	outcomes := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, o, _ := fw.Do(context.Background(), callFresh, local.fn)
			outcomes <- o
		}()
		time.Sleep(30 * time.Millisecond) // the first caller reaches Acquire first
	}
	wg.Wait()
	close(outcomes)
	got := map[string]int{}
	for o := range outcomes {
		got[o]++
	}
	if got["acquired"] != 1 || got["ok"] != 1 || len(got) != 2 {
		t.Fatalf("outcomes %v, want one acquired and one ok", got)
	}
	if f.out(forward.KindFreshState, "unavailable") != 0 || local.n.Load() != 1 || len(f.runners["pod-a"].Calls()) != 1 {
		t.Errorf("unavailable=%v local runs=%d owner runs=%d, want 0, 1 (the winner) and 1 (the loser, forwarded)", f.out(forward.KindFreshState, "unavailable"), local.n.Load(), len(f.runners["pod-a"].Calls()))
	}
}

// The call that runs after an acquire carries the new epoch's fence token, so the owner guard and the permit see it.
func TestAcquiredCallCarriesTheNewEpochToken(t *testing.T) {
	f := newFleet(t)
	f.view.SetOwner(group, forward.LeaseOwner{})
	want := fence.Token{Group: group, Pod: "pod-c", Epoch: 12}
	fw := f.requester(func(cfg *forward.RequesterConfig) {
		cfg.AcquireOnRequest = true
		cfg.Acquire = func(context.Context, string) (bool, error) { return true, nil }
		cfg.Token = func(g string) (fence.Token, bool) { return want, g == group }
	})
	var got fence.Token
	var has bool
	_, outcome, _ := fw.Do(context.Background(), callFresh, func(ctx context.Context) (forward.Result, error) {
		got, has = fence.From(ctx)
		return forward.Result{Status: 200, Body: json.RawMessage(`{}`)}, nil
	})
	if outcome != "acquired" || !has || got != want {
		t.Fatalf("outcome=%q token=%+v (has %v), want the new epoch's token %+v", outcome, got, has, want)
	}
}

// A call served by the pod that already holds the group carries the group's fence token too, so the Rivian permit check passes it.
func TestHeldCallCarriesTheGroupsToken(t *testing.T) {
	f := newFleet(t)
	f.view.HeldAt = map[string]int64{group: 7}
	want := fence.Token{Group: group, Pod: "pod-a", Epoch: 7}
	fw := f.requester(func(cfg *forward.RequesterConfig) {
		cfg.Token = func(g string) (fence.Token, bool) { return want, g == group }
	})
	var got fence.Token
	var has bool
	_, outcome, _ := fw.Do(context.Background(), callFresh, func(ctx context.Context) (forward.Result, error) {
		got, has = fence.From(ctx)
		return forward.Result{Status: 200, Body: json.RawMessage(`{}`)}, nil
	})
	if outcome != "held" || !has || got != want {
		t.Fatalf("outcome=%q token=%+v (has %v), want held with %+v", outcome, got, has, want)
	}
}

var callPlan = func(mode string) forward.Call {
	return forward.Call{Kind: forward.KindPlanLegs, User: user, Group: group, Args: forward.PlanLegsArgs{
		VehicleID: "v-1", StartingSoC: 80, DriveMode: mode,
		Waypoints: []forward.Waypoint{{Latitude: 34, Longitude: -118}, {Latitude: 35, Longitude: -117}}}}
}

// Arguments the owner would refuse are not sent at all: the call is served the fail-soft way at once, counted `rejected`, and the user does
// not wait out the kind's timeout (28 s for plan_legs) for an answer that cannot come. Every drive mode the trip handlers let through is
// sent.
func TestRequesterRefusesBadArgsBeforeSending(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	start := time.Now()
	var local localCounter
	_, outcome, err := f.requester(nil).Do(context.Background(), callPlan("NOT_A_MODE"), local.fn)
	if err != nil || outcome != "rejected" || local.n.Load() != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("outcome=%q local=%d err=%v after %v, want rejected, served locally, at once", outcome, local.n.Load(), err, time.Since(start))
	}
	if n, _ := f.rdb.LLen(context.Background(), f.keys.Queue("pod-a")).Result(); n != 0 || len(f.runners["pod-a"].Calls()) != 0 {
		t.Errorf("a refused request reached the owner (queue %d, runs %d)", n, len(f.runners["pod-a"].Calls()))
	}
	if f.out(forward.KindPlanLegs, "rejected") != 1 || f.out(forward.KindPlanLegs, "timeout") != 0 {
		t.Errorf("rejected=%v timeout=%v, want 1 and 0", f.out(forward.KindPlanLegs, "rejected"), f.out(forward.KindPlanLegs, "timeout"))
	}
	for _, mode := range []string{"", "EVERYDAY", "DISTANCE", "SPORT", "WINTER", "OFF_ROAD_AUTO"} {
		var l localCounter
		if _, outcome, err := f.requester(nil).Do(context.Background(), callPlan(mode), l.fn); err != nil || outcome != "ok" || l.n.Load() != 0 {
			t.Errorf("drive mode %q: outcome=%q local=%d err=%v, want it forwarded and served by the owner", mode, outcome, l.n.Load(), err)
		}
	}
}

// RIVOLT_ROLE api/jobs (NoLocal): every case that falls soft on an all pod is a 503 here, counted no_local. The call never runs on this pod
// and the acquire is never asked, even with acquire-on-request set.
func TestRequesterNoLocalNeverRunsHere(t *testing.T) {
	for name, c := range map[string]struct {
		setup func(f *fleet)
		call  forward.Call
	}{
		"no owner":               {func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{}) }, callFresh},
		"ready marker missing":   {func(f *fleet) { /* the lease row names pod-a, which runs no loop */ }, callFresh},
		"redis down":             {func(f *fleet) { f.owner("pod-a", 7, true, nil); f.mr.Close() }, callFresh},
		"lease read fails":       {func(f *fleet) { f.view.OwnerErr = errors.New("db down") }, callFresh},
		"the row names this pod": {func(f *fleet) { f.view.SetOwner(group, forward.LeaseOwner{Found: true, Pod: "pod-c", Epoch: 7}) }, callFresh},
		"moved, no new owner":    {func(f *fleet) { f.owner("pod-a", 7, false, nil) }, callFresh},
		"moved, row gone":        {func(f *fleet) { f.owner("pod-a", 7, false, nil); f.view.DeleteOwner(group) }, callFresh},
		"bad args":               {func(f *fleet) { f.owner("pod-a", 7, true, nil) }, callPlan("NOT_A_MODE")},
	} {
		for _, acquire := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/acquire=%v", name, acquire), func(t *testing.T) {
				f := newFleet(t)
				c.setup(f)
				var local localCounter
				fw := f.requester(func(cfg *forward.RequesterConfig) {
					cfg.NoLocal = true
					cfg.AcquireOnRequest = acquire
					cfg.Acquire = func(context.Context, string) (bool, error) {
						t.Error("acquire asked on a no-local pod")
						return true, nil
					}
				})
				res, outcome, err := fw.Do(context.Background(), c.call, local.fn)
				if err != nil || outcome != "no_local" || res.Status != forward.StatusUnavailable || local.n.Load() != 0 {
					t.Fatalf("outcome=%q res=%+v local=%d err=%v, want no_local, 503, no local run", outcome, res, local.n.Load(), err)
				}
				if f.out(c.call.Kind, "no_local") != 1 || f.out(c.call.Kind, "local") != 0 || f.out(c.call.Kind, "acquired") != 0 {
					t.Errorf("no_local=%v local=%v acquired=%v, want 1, 0, 0", f.out(c.call.Kind, "no_local"), f.out(c.call.Kind, "local"), f.out(c.call.Kind, "acquired"))
				}
			})
		}
	}
}

// Busy twice on a no-local pod is a 503 counted no_local, never local_busy.
func TestRequesterNoLocalBusyIs503(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	release := make(chan struct{})
	f.runners["pod-a"].Gate = release
	fw := f.requester(func(cfg *forward.RequesterConfig) { cfg.NoLocal = true })
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _, _ = fw.Do(context.Background(), callFresh, (&localCounter{}).fn) }()
	}
	for dl := time.Now().Add(3 * time.Second); len(f.runners["pod-a"].Calls()) < 2 && time.Now().Before(dl); {
		time.Sleep(5 * time.Millisecond)
	}
	var local localCounter
	res, outcome, _ := fw.Do(context.Background(), callFresh, local.fn)
	close(release)
	wg.Wait()
	if outcome != "no_local" || res.Status != forward.StatusUnavailable || local.n.Load() != 0 {
		t.Fatalf("outcome=%q res=%+v local=%d, want no_local, 503, no local run", outcome, res, local.n.Load())
	}
	if f.out(forward.KindFreshState, "local_busy") != 0 {
		t.Error("local_busy counted on a no-local pod")
	}
}

// A forwardable call on a no-local pod still goes to the owner as usual.
func TestRequesterNoLocalStillForwards(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	var local localCounter
	res, outcome, err := f.requester(func(cfg *forward.RequesterConfig) { cfg.NoLocal = true }).Do(context.Background(), callFresh, local.fn)
	if err != nil || outcome != "ok" || string(res.Body) != `{"served_by":"pod-a"}` || local.n.Load() != 0 {
		t.Fatalf("outcome=%q res=%+v local=%d err=%v", outcome, res, local.n.Load(), err)
	}
}

// NoGroup on a no-local pod tells the handler not to serve the call here and counts no_local.
func TestNoGroupOnANoLocalPod(t *testing.T) {
	f := newFleet(t)
	if f.requester(func(cfg *forward.RequesterConfig) { cfg.NoLocal = true }).NoGroup(forward.KindFreshState) {
		t.Error("NoGroup let a no-local pod serve the call")
	}
	if f.out(forward.KindFreshState, "no_local") != 1 || f.out(forward.KindFreshState, "no_group") != 0 {
		t.Errorf("no_local=%v no_group=%v, want 1 and 0", f.out(forward.KindFreshState, "no_local"), f.out(forward.KindFreshState, "no_group"))
	}
	if !f.requester(nil).NoGroup(forward.KindFreshState) {
		t.Error("NoGroup refused on an all pod")
	}
}

func (f *fleet) drain(outcome string) float64 {
	return testutil.ToFloat64(f.m.DrainWait.WithLabelValues(outcome))
}

// drainingFleet has pod-a holding the group with the marker saying "draining": it stopped taking work and still holds the lease row.
func drainingFleet(t *testing.T) *fleet {
	f := newFleet(t)
	f.owner("pod-a", 7, true, nil)
	if err := f.mr.Set(f.keys.Ready("pod-a"), "draining"); err != nil {
		t.Fatal(err)
	}
	return f
}

func waitingRequester(f *fleet, acquire bool, wait time.Duration) *forward.Forwarder {
	return f.requester(func(cfg *forward.RequesterConfig) {
		cfg.NoLocal = true
		cfg.AcquireOnRequest = acquire
		cfg.OwnerWait = wait
		cfg.OwnerPoll = 20 * time.Millisecond
		cfg.OwnerLeaseEvery = 20 * time.Millisecond
	})
}

// A draining owner keeps its lease row until it has released: a call in that gap on a no-local pod waits for the successor and is served
// by it, never a 503, and nothing is sent to the old owner (the kind runs once).
func TestRequesterWaitsThroughADrainingOwner(t *testing.T) {
	for _, acquire := range []bool{false, true} {
		t.Run(fmt.Sprintf("acquire=%v", acquire), func(t *testing.T) {
			f := drainingFleet(t)
			go func() {
				time.Sleep(300 * time.Millisecond)
				f.owner("pod-b", 8, true, nil)
				f.view.SetOwner(group, forward.LeaseOwner{Found: true, Pod: "pod-b", Epoch: 8})
			}()
			var local localCounter
			res, outcome, err := waitingRequester(f, acquire, 5*time.Second).Do(context.Background(), callFresh, local.fn)
			if err != nil || outcome != "ok" || string(res.Body) != `{"served_by":"pod-b"}` || local.n.Load() != 0 {
				t.Fatalf("outcome=%q res=%+v local=%d err=%v, want ok from pod-b", outcome, res, local.n.Load(), err)
			}
			if got := len(f.runners["pod-a"].Calls()); got != 0 {
				t.Errorf("the draining owner ran %d calls, want 0", got)
			}
			if f.drain("forwarded") != 1 || f.drain("timeout") != 0 || f.drain("canceled") != 0 {
				t.Errorf("drain wait forwarded=%v timeout=%v canceled=%v, want 1 0 0", f.drain("forwarded"), f.drain("timeout"), f.drain("canceled"))
			}
		})
	}
}

// A crashed owner leaves no marker: the call takes today's immediate path (503 on a no-local pod), no wait.
func TestRequesterCrashedOwnerIsImmediate(t *testing.T) {
	f := newFleet(t) // the row names pod-a, which runs no loop: no marker
	start := time.Now()
	res, outcome, _ := waitingRequester(f, false, 5*time.Second).Do(context.Background(), callFresh, (&localCounter{}).fn)
	if outcome != "no_local" || res.Status != forward.StatusUnavailable || time.Since(start) > time.Second {
		t.Fatalf("outcome=%q status=%d after %v, want an immediate no_local 503", outcome, res.Status, time.Since(start))
	}
	if f.drain("forwarded")+f.drain("timeout")+f.drain("canceled") != 0 {
		t.Error("a crash must not count as a drain wait")
	}
}

// A draining owner whose successor never appears is a 503 after the cap, not a hang, and the cap is the kind's own when it is shorter.
func TestRequesterDrainWaitCapIs503(t *testing.T) {
	f := drainingFleet(t)
	start := time.Now()
	res, outcome, _ := waitingRequester(f, false, 300*time.Millisecond).Do(context.Background(), callFresh, (&localCounter{}).fn)
	if outcome != "no_local" || res.Status != forward.StatusUnavailable || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("outcome=%q status=%d after %v, want no_local 503 after the wait", outcome, res.Status, time.Since(start))
	}
	if f.drain("timeout") != 1 {
		t.Errorf("drain timeout = %v, want 1", f.drain("timeout"))
	}
}

// A request that ends during the wait stops waiting at once.
func TestRequesterDrainWaitCanceled(t *testing.T) {
	f := drainingFleet(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, _, _ = waitingRequester(f, false, 5*time.Second).Do(ctx, callFresh, (&localCounter{}).fn)
	if time.Since(start) > time.Second {
		t.Errorf("a canceled request waited %v", time.Since(start))
	}
	if f.drain("canceled") != 1 {
		t.Errorf("drain canceled = %v, want 1", f.drain("canceled"))
	}
}

// The wait never eats the route's budget: it is capped by what the deadline leaves after the kind's timeout and 2 s, and live_session's own
// cap keeps it near 4 s.
func TestWaitBudget(t *testing.T) {
	cases := []struct {
		kind      forward.Kind
		remaining time.Duration
		want      time.Duration
	}{
		{forward.KindFreshState, 0, 12 * time.Second},
		{forward.KindFreshState, 30 * time.Second, 12 * time.Second},
		{forward.KindFreshState, 20 * time.Second, 20*time.Second - forward.Kinds[forward.KindFreshState].Timeout - 2*time.Second},
		{forward.KindLiveSession, 30 * time.Second, 4 * time.Second},
		{forward.KindFreshState, time.Second, 0},
	}
	for _, c := range cases {
		if got := forward.WaitBudget(c.kind, c.remaining, 12*time.Second); got != c.want {
			t.Errorf("WaitBudget(%s, %v) = %v, want %v", c.kind, c.remaining, got, c.want)
		}
	}
}

// countView counts the lease-row reads.
type countView struct {
	*switchView
	n atomic.Int32
}

func (c *countView) Owner(ctx context.Context, group string) (forward.LeaseOwner, error) {
	c.n.Add(1)
	return c.switchView.Owner(ctx, group)
}

// N waiters on one draining owner share one poller: the lease row is read about once per OwnerLeaseEvery, not once per waiter per poll.
func TestRequesterWaitersShareOnePoller(t *testing.T) {
	f := drainingFleet(t)
	cv := &countView{switchView: f.view}
	fw := f.requester(func(cfg *forward.RequesterConfig) {
		cfg.NoLocal = true
		cfg.Leases = cv
		cfg.OwnerWait = 1200 * time.Millisecond
		cfg.OwnerPoll = 20 * time.Millisecond
		cfg.OwnerLeaseEvery = 400 * time.Millisecond
	})
	const waiters = 20
	var wg sync.WaitGroup
	for range waiters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = fw.Do(context.Background(), callFresh, (&localCounter{}).fn)
		}()
	}
	wg.Wait()
	// Each waiter reads the row once to find the owner; the poller adds at most one read per OwnerLeaseEvery (about 3 in 1.2 s).
	if got := cv.n.Load(); got > waiters+5 {
		t.Errorf("%d lease reads for %d waiters, want at most %d: the waiters must share one poller", got, waiters, waiters+5)
	}
}
