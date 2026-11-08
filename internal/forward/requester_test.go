package forward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

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

// moved with the owner unchanged: no second try, a 503, counted moved. The call is not run twice anywhere.
func TestRequesterMovedWithNoNewOwnerIs503(t *testing.T) {
	f := newFleet(t)
	f.owner("pod-a", 7, false, nil)
	res, outcome, _ := f.requester(nil).Do(context.Background(), callFresh, (&localCounter{}).fn)
	if outcome != "moved" || res.Status != forward.StatusUnavailable {
		t.Fatalf("outcome=%q res=%+v", outcome, res)
	}
	if f.out(forward.KindFreshState, "moved") != 1 || len(f.runners["pod-a"].Calls()) != 0 {
		t.Error("moved counted once, nothing run")
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
