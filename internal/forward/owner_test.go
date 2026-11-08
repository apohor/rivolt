package forward_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/redis/go-redis/v9"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/forward/forwardtest"
)

type rig struct {
	t      *testing.T
	mr     *miniredis.Miniredis
	rdb    *redis.Client
	signer *forward.Signer
	keys   forward.Keys
	leases *forwardtest.LeaseView
	runner *forwardtest.Runner
	m      *forward.Metrics
	owner  *forward.Owner
	stop   context.CancelFunc
	done   chan struct{}
}

const (
	pod   = "pod-a"
	group = "11111111-2222-3333-4444-555555555555"
	user  = "11111111-2222-3333-4444-555555555555"
)

func newRig(t *testing.T, mut func(*forward.OwnerConfig)) *rig {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr(), ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = rdb.Close() })
	signer, err := forward.NewSigner(bytes.Repeat([]byte{7}, forward.KeyLen))
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, mr: mr, rdb: rdb, signer: signer, keys: forward.Keys{},
		leases: &forwardtest.LeaseView{
			Owners: map[string]forward.LeaseOwner{group: {Found: true, Pod: pod, Epoch: 7}},
			HeldAt: map[string]int64{group: 7},
		},
		runner: &forwardtest.Runner{}, m: forward.NewMetrics(prometheus.NewRegistry())}
	cfg := forward.OwnerConfig{Pod: pod, Keys: r.keys, Signer: signer, Redis: rdb, Leases: r.leases, Runner: r.runner, Metrics: r.m, BlockFor: time.Second}
	if mut != nil {
		mut(&cfg)
	}
	o, err := forward.NewOwner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.owner = o
	return r
}

func (r *rig) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.stop = cancel
	r.done = make(chan struct{})
	go func() { defer close(r.done); r.owner.Run(ctx) }()
	r.t.Cleanup(r.shutdown)
}

func (r *rig) shutdown() {
	if r.stop != nil {
		r.stop()
		<-r.done
		r.stop = nil
	}
}

// push enqueues a request signed by the rig's key. expires is relative to Redis's clock.
func (r *rig) push(kind forward.Kind, args string, mut func(*forward.Request)) *forward.Request {
	r.t.Helper()
	id, _ := forward.NewID()
	now, err := r.rdb.Time(context.Background()).Result()
	if err != nil {
		r.t.Fatal(err)
	}
	req := &forward.Request{V: forward.Version, ID: id, User: user, Group: group, Epoch: 7, Kind: kind, Args: json.RawMessage(args),
		ExpiresMS: forward.ExpiresMS(now.UnixMilli(), kind)}
	if mut != nil {
		mut(req)
	}
	r.signer.SignRequest(req)
	b, _ := json.Marshal(req)
	r.pushRaw(b)
	return req
}

func (r *rig) pushRaw(b []byte) {
	r.t.Helper()
	if err := r.rdb.RPush(context.Background(), r.keys.Queue(pod), b).Err(); err != nil {
		r.t.Fatal(err)
	}
}

// reply waits for the request's reply, verifies its signature and returns it (nil if none arrives in 1 s).
func (r *rig) reply(req *forward.Request) *forward.Reply {
	r.t.Helper()
	res, err := r.rdb.BLPop(context.Background(), time.Second, r.keys.Reply(req.ID)).Result()
	if err != nil {
		return nil
	}
	rep, derr := forward.DecodeReply([]byte(res[1]))
	if derr != nil {
		r.t.Fatalf("undecodable reply: %v", derr)
	}
	if err := r.signer.VerifyReply(rep); err != nil {
		r.t.Fatalf("a reply must carry a valid signature: %v", err)
	}
	return rep
}

func (r *rig) waitQueueEmpty() {
	r.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := r.rdb.LLen(context.Background(), r.keys.Queue(pod)).Result(); n == 0 {
			time.Sleep(150 * time.Millisecond) // let the in-flight element finish
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatal("the queue did not drain")
}

func count(r *rig, kind forward.Kind, outcome string) float64 {
	return testutil.ToFloat64(r.m.Total.WithLabelValues(string(kind), outcome))
}

const planArgsJSON = `{"vehicle_id":"v-1","starting_soc":80,"starting_range_meters":0,"origin_bearing":0,"waypoints":[{"lat":34,"lon":-118},{"lat":35,"lon":-117}]}`

const fresh = `{"vehicle_id":"v-1","max_age_ms":30000}`

func TestOwnerRunsAKindAndAnswersWithASignedReply(t *testing.T) {
	r := newRig(t, nil)
	r.runner.Reply = forward.Result{Status: 200, Body: json.RawMessage(`{"soc":71}`)}
	r.start()
	req := r.push(forward.KindFreshState, fresh, nil)
	rep := r.reply(req)
	if rep == nil || !rep.OK || rep.Status != 200 || string(rep.Body) != `{"soc":71}` || rep.ID != req.ID || rep.User != req.User {
		t.Fatalf("reply = %+v", rep)
	}
	calls := r.runner.Calls()
	if len(calls) != 1 || calls[0].Kind != forward.KindFreshState || calls[0].User != user || calls[0].Group != group {
		t.Fatalf("runner calls = %+v", calls)
	}
	if a, ok := calls[0].Args.(*forward.FreshStateArgs); !ok || a.VehicleID != "v-1" {
		t.Errorf("the runner gets the validated args, got %#v", calls[0].Args)
	}
	if ttl := r.mr.TTL(r.keys.Reply(req.ID)); ttl > forward.ReplyTTL*time.Second {
		t.Errorf("reply ttl = %v, want at most %d s", ttl, forward.ReplyTTL)
	}
	if count(r, forward.KindFreshState, "ok") != 1 {
		t.Error("ok counted")
	}
}

// T7: an error class survives the round trip so the requester maps it with the handlers' own mapping.
func TestOwnerReplyKeepsStatusAndClass(t *testing.T) {
	r := newRig(t, nil)
	r.runner.Reply = forward.Result{Status: 429, Class: "quota", Body: json.RawMessage(`{"retry_after":30}`)}
	r.start()
	rep := r.reply(r.push(forward.KindFreshState, fresh, nil))
	if rep == nil || rep.OK || rep.Status != 429 || rep.Class != "quota" || string(rep.Body) != `{"retry_after":30}` {
		t.Fatalf("reply = %+v", rep)
	}
}

// T4: an expired item is dropped without running and nobody is answered.
func TestOwnerDropsAnExpiredRequest(t *testing.T) {
	r := newRig(t, nil)
	r.start()
	req := r.push(forward.KindFreshState, fresh, func(q *forward.Request) { q.ExpiresMS = 1 })
	r.waitQueueEmpty()
	if len(r.runner.Calls()) != 0 {
		t.Fatal("an expired request must not run")
	}
	if r.reply(req) != nil {
		t.Error("an expired request gets no reply: the requester has given up")
	}
	if count(r, forward.KindFreshState, "timeout") != 1 {
		t.Error("timeout counted")
	}
}

// T5: a request for a group this pod does not hold at that epoch, past the local deadline, or whose lease row the database does not
// show as this pod's, is answered `moved` and never run.
func TestOwnerAnswersMovedAndNeverRuns(t *testing.T) {
	for name, tweak := range map[string]func(*rig, *forward.Request){
		"epoch mismatch":      func(r *rig, q *forward.Request) { q.Epoch = 6 },
		"group not held":      func(r *rig, q *forward.Request) { q.Group = "99999999-2222-3333-4444-555555555555" },
		"past local deadline": func(r *rig, q *forward.Request) { r.leases.SetPast(true) },
		"row shows another pod": func(r *rig, q *forward.Request) {
			r.leases.SetOwner(group, forward.LeaseOwner{Found: true, Pod: "pod-b", Epoch: 7})
		},
		"row is gone (deleted user)": func(r *rig, q *forward.Request) { r.leases.DeleteOwner(group) },
		"row at another epoch": func(r *rig, q *forward.Request) {
			r.leases.SetOwner(group, forward.LeaseOwner{Found: true, Pod: pod, Epoch: 8})
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.start()
			req := r.push(forward.KindFreshState, fresh, nil)
			_ = req
			// push already enqueued with epoch 7 / the rig's group; for the tweaks that change the request, push a second one.
			r.waitQueueEmpty()
			q2 := r.push(forward.KindFreshState, fresh, func(q *forward.Request) { tweak(r, q) })
			rep := r.reply(q2)
			if rep == nil || rep.OK || rep.Status != forward.StatusMoved || rep.Class != forward.ClassMoved {
				t.Fatalf("reply = %+v, want moved", rep)
			}
			if got := len(r.runner.Calls()); got != 1 { // the first, healthy request ran; the tweaked one did not
				t.Fatalf("runner calls = %d, want 1 (the tweaked request must not run)", got)
			}
			if count(r, forward.KindFreshState, "moved") != 1 {
				t.Error("moved counted")
			}
		})
	}
}

// T12: nothing that fails admission runs, none is answered, all are counted rejected.
func TestOwnerRejectsWhatFailsAdmission(t *testing.T) {
	r := newRig(t, nil)
	r.start()
	other, _ := forward.NewSigner(bytes.Repeat([]byte{9}, forward.KeyLen))
	signedBy := func(s *forward.Signer, kind forward.Kind, args string) []byte {
		id, _ := forward.NewID()
		now, _ := r.rdb.Time(context.Background()).Result()
		q := &forward.Request{V: forward.Version, ID: id, User: user, Group: group, Epoch: 7, Kind: kind, Args: json.RawMessage(args), ExpiresMS: forward.ExpiresMS(now.UnixMilli(), forward.KindFreshState)}
		s.SignRequest(q)
		b, _ := json.Marshal(q)
		return b
	}
	good := signedBy(r.signer, forward.KindFreshState, fresh)
	missingSig := bytes.Replace(good, []byte(`"sig":"`), []byte(`"sig":"00`), 1)
	for name, b := range map[string][]byte{
		"signed by another key":        signedBy(other, forward.KindFreshState, fresh),
		"a bad signature":              missingSig,
		"signed, kind not allowlisted": signedBy(r.signer, forward.Kind("exec"), `{}`),
		"signed, args fail the schema": signedBy(r.signer, forward.KindFreshState, `{"vehicle_id":"v/../1","max_age_ms":1}`),
		"signed, adopt with a token":   signedBy(r.signer, forward.KindAdopt, `{"token":"x"}`),
		"garbage":                      []byte("not json at all"),
		"oversize":                     append(bytes.Repeat([]byte{' '}, forward.MaxRequestBytes), good...),
		"wrong version":                bytes.Replace(good, []byte(`"v":1`), []byte(`"v":2`), 1),
	} {
		r.pushRaw(b)
		_ = name
	}
	r.waitQueueEmpty()
	if len(r.runner.Calls()) != 0 {
		t.Fatalf("nothing that fails admission may run, got %+v", r.runner.Calls())
	}
	var total float64
	for _, k := range forward.MetricKinds() {
		total += testutil.ToFloat64(r.m.Total.WithLabelValues(k, "rejected"))
	}
	if total != 8 {
		t.Errorf("rejected = %v, want 8", total)
	}
	keys, _ := r.rdb.Keys(context.Background(), r.keys.Reply("*")).Result()
	if len(keys) != 0 {
		t.Errorf("a rejected message must get no reply, found %v", keys)
	}
}

// A runner that fails (not a timeout) is counted `error`, never `ok`: "ok" means served. The requester gets a signed 500 `internal`.
func TestOwnerRunnerErrorIsItsOwnOutcome(t *testing.T) {
	r := newRig(t, nil)
	r.runner.Err = context.Canceled // any non-timeout failure
	r.start()
	rep := r.reply(r.push(forward.KindFreshState, fresh, nil))
	if rep == nil || rep.OK || rep.Status != forward.StatusInternal || rep.Class != forward.ClassInternal {
		t.Fatalf("reply = %+v, want a signed 500 internal", rep)
	}
	if count(r, forward.KindFreshState, "error") != 1 || count(r, forward.KindFreshState, "ok") != 0 {
		t.Errorf("a failed run counts error (%v) and not ok (%v)", count(r, forward.KindFreshState, "error"), count(r, forward.KindFreshState, "ok"))
	}
}

// adopt is best effort: it runs, and no reply is written.
func TestOwnerAdoptRunsWithoutAReply(t *testing.T) {
	r := newRig(t, nil)
	r.start()
	req := r.push(forward.KindAdopt, `{}`, nil)
	r.waitQueueEmpty()
	if c := r.runner.Calls(); len(c) != 1 || c[0].Kind != forward.KindAdopt {
		t.Fatalf("calls = %+v", c)
	}
	if r.reply(req) != nil {
		t.Error("adopt writes no reply")
	}
}

// The per-user cap: a user's third concurrent kind is answered busy while the first two run.
func TestOwnerCapsKindsPerUser(t *testing.T) {
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	r := newRig(t, nil)
	blocking := &blockingRunner{release: release, started: func() { mu.Lock(); started++; mu.Unlock() }}
	o, err := forward.NewOwner(forward.OwnerConfig{Pod: pod, Keys: r.keys, Signer: r.signer, Redis: r.rdb, Leases: r.leases, Runner: blocking, Metrics: r.m, BlockFor: time.Second, PerUser: 2})
	if err != nil {
		t.Fatal(err)
	}
	r.owner = o
	r.start()
	a := r.push(forward.KindFreshState, fresh, nil)
	b := r.push(forward.KindFreshState, fresh, nil)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := started
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	c := r.push(forward.KindFreshState, fresh, nil)
	rep := r.reply(c)
	if rep == nil || rep.Status != forward.StatusBusy || rep.Class != forward.ClassBusy {
		t.Fatalf("third request: %+v, want busy", rep)
	}
	close(release)
	if ra, rb := r.reply(a), r.reply(b); ra == nil || rb == nil || !ra.OK || !rb.OK {
		t.Fatalf("the first two must complete: %+v %+v", ra, rb)
	}
	// With the first two done, the user can run again.
	d := r.push(forward.KindFreshState, fresh, nil)
	if rd := r.reply(d); rd == nil || !rd.OK {
		t.Errorf("after the others finish, a new request runs: %+v", rd)
	}
}

type blockingRunner struct {
	release chan struct{}
	started func()
}

func (b *blockingRunner) Run(ctx context.Context, _ forward.Kind, _, _ string, _ any) (forward.Result, error) {
	b.started()
	select {
	case <-b.release:
	case <-ctx.Done():
		return forward.Result{}, ctx.Err()
	}
	return forward.Result{Status: 200, Body: json.RawMessage(`{}`)}, nil
}

// The readiness marker exists while the loop runs, with its TTL, and is gone after it stops.
func TestOwnerReadinessMarker(t *testing.T) {
	r := newRig(t, nil)
	key := r.keys.Ready(pod)
	r.start()
	deadline := time.Now().Add(2 * time.Second)
	for !r.mr.Exists(key) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !r.mr.Exists(key) {
		t.Fatal("the readiness marker must be set while the owner loop runs")
	}
	if ttl := r.mr.TTL(key); ttl <= 0 || ttl > forward.ReadyTTL*time.Second {
		t.Errorf("marker ttl = %v, want within (0, %d s]", ttl, forward.ReadyTTL)
	}
	if testutil.ToFloat64(r.m.OwnerReady) != 1 {
		t.Error("owner_ready gauge is 1 while running")
	}
	r.shutdown()
	if r.mr.Exists(key) {
		t.Error("the marker must be removed when the loop stops, so requesters stop forwarding here")
	}
	if testutil.ToFloat64(r.m.OwnerReady) != 0 {
		t.Error("owner_ready gauge is 0 after stopping")
	}
}

func TestNewOwnerValidates(t *testing.T) {
	r := newRig(t, nil)
	for name, mut := range map[string]func(*forward.OwnerConfig){
		"no signer": func(c *forward.OwnerConfig) { c.Signer = nil },
		"no redis":  func(c *forward.OwnerConfig) { c.Redis = nil },
		"no leases": func(c *forward.OwnerConfig) { c.Leases = nil },
		"no runner": func(c *forward.OwnerConfig) { c.Runner = nil },
		"bad pod":   func(c *forward.OwnerConfig) { c.Pod = "a:b" },
		"empty pod": func(c *forward.OwnerConfig) { c.Pod = "" },
	} {
		cfg := forward.OwnerConfig{Pod: pod, Signer: r.signer, Redis: r.rdb, Leases: r.leases, Runner: r.runner}
		mut(&cfg)
		if _, err := forward.NewOwner(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	_ = strings.Repeat
}

// An answer that does not fit the reply cap is counted `too_big`, never `ok`: the user gets an error, and the metric must not call it success.
func TestOwnerOversizeAnswerIsItsOwnOutcome(t *testing.T) {
	r := newRig(t, nil)
	r.runner.Reply = forward.Result{Status: 200, Body: json.RawMessage(`"` + strings.Repeat("x", forward.MaxReplyBytes) + `"`)}
	r.start()
	rep := r.reply(r.push(forward.KindPlanLegs, planArgsJSON, nil))
	if rep == nil || rep.OK || rep.Status != forward.StatusTooBig || rep.Class != forward.ClassReplyTooBig {
		t.Fatalf("reply = %+v, want a signed reply_too_big", rep)
	}
	if count(r, forward.KindPlanLegs, "too_big") != 1 || count(r, forward.KindPlanLegs, "ok") != 0 {
		t.Errorf("too_big=%v ok=%v, want 1 and 0", count(r, forward.KindPlanLegs, "too_big"), count(r, forward.KindPlanLegs, "ok"))
	}
}
