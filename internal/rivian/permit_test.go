package rivian

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/fence"
)

func withMode(t *testing.T, m PermitMode) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	SetPermitMode(m)
	SetUnpermittedObserver(func(string) { n.Add(1) })
	t.Cleanup(func() { SetPermitMode(PermitOff); SetUnpermittedObserver(nil) })
	return &n
}

func TestParsePermitMode(t *testing.T) {
	for in, want := range map[string]PermitMode{"": PermitOff, "off": PermitOff, "garbage": PermitOff, "observe": PermitObserve, " Observe ": PermitObserve, "enforce": PermitEnforce, "ENFORCE": PermitEnforce} {
		if got := ParsePermitMode(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

// A request with a fence token or a holder marker is permitted in every mode; one with neither is counted in observe and refused in enforce.
func TestCheckPermitByMode(t *testing.T) {
	tok := fence.With(context.Background(), fence.Token{Group: "g", Pod: "p", Epoch: 1})
	for _, h := range []string{HolderLogin, HolderMFA, HolderLogout, HolderDeleteRevoke, HolderForwardRun} {
		marked := WithCallPermit(context.Background(), h)
		for _, mode := range []PermitMode{PermitOff, PermitObserve, PermitEnforce} {
			n := withMode(t, mode)
			if checkPermit(marked, "GetVehicleState") != nil || checkPermit(tok, "GetVehicleState") != nil || n.Load() != 0 {
				t.Errorf("%s/%v: a permitted call was refused or counted", h, mode)
			}
		}
	}
	bare := context.Background()
	n := withMode(t, PermitOff)
	if checkPermit(bare, "X") != nil || n.Load() != 0 {
		t.Error("off: must not check or count")
	}
	n = withMode(t, PermitObserve)
	if err := checkPermit(bare, "X"); err != nil || n.Load() != 1 {
		t.Errorf("observe: err=%v counted=%d, want the call allowed and counted once", err, n.Load())
	}
	n = withMode(t, PermitEnforce)
	if err := checkPermit(bare, "X"); !errors.Is(err, ErrNoPermit) || n.Load() != 1 {
		t.Errorf("enforce: err=%v counted=%d, want ErrNoPermit and counted once", err, n.Load())
	}
}

// Every operation name in the source is in PermitOps, so the counter's label set is complete (a new operation fails here until it is listed).
func TestPermitOpsCoverEveryOperation(t *testing.T) {
	entries, _ := os.ReadDir(".")
	re := regexp.MustCompile(`OperationName: *"([A-Za-z0-9_]+)"`)
	known := map[string]bool{}
	for _, o := range PermitOps {
		known[o] = true
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, _ := os.ReadFile(e.Name())
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if !known[m[1]] {
				t.Errorf("%s: operation %q is not in PermitOps (and metrics.RivianPermitOps)", e.Name(), m[1])
			}
		}
	}
	if PermitOp("not_an_operation") != "other" || PermitOp("Login") != "Login" {
		t.Error("PermitOp must map unknown names to other and keep known ones")
	}
}

// The three places the client sends a request to Rivian are the only three, and each one asks for the permit. A new network origin (or one that
// forgot to ask) fails here, so the permit cannot quietly leave a path out.
func TestEveryNetworkOriginAsksForThePermit(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ origins, asks int }{
		"live.go":   {origins: 2, asks: 2},
		"ws_mux.go": {origins: 1, asks: 1},
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "permit.go" {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		origins := strings.Count(src, "http.NewRequestWithContext(") + strings.Count(src, "http.NewRequest(") + strings.Count(src, "websocket.Dial(")
		asks := strings.Count(src, "checkPermit(")
		w := want[name]
		if origins != w.origins || asks != w.asks {
			t.Errorf("%s: %d network origins and %d permit checks, want %d and %d: a new request path must ask checkPermit first (and be listed here)", name, origins, asks, w.origins, w.asks)
		}
	}
}

// End to end through the real client: with the permit enforced, a bare context never reaches the server and a marked one does.
func TestEnforcedPermitStopsTheRequest(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"c","appSessionToken":"a"}}}`)
	}))
	defer server.Close()
	n := withMode(t, PermitEnforce)
	c := NewLive()
	c.endpoint = server.URL + "/gateway"

	_, err := doGraphQLAt[map[string]any](context.Background(), c, c.endpoint, graphQLRequest{OperationName: "CreateCSRFToken"}, nil)
	if !errors.Is(err, ErrNoPermit) || hits.Load() != 0 || n.Load() != 1 {
		t.Fatalf("bare: err=%v hits=%d counted=%d, want ErrNoPermit, no request, counted once", err, hits.Load(), n.Load())
	}
	_, err = doGraphQLAt[map[string]any](WithCallPermit(context.Background(), HolderLogin), c, c.endpoint, graphQLRequest{OperationName: "CreateCSRFToken"}, nil)
	if errors.Is(err, ErrNoPermit) || hits.Load() != 1 {
		t.Fatalf("marked: err=%v hits=%d, want the request sent", err, hits.Load())
	}
	// Observe sends it anyway and counts.
	n = withMode(t, PermitObserve)
	_, _ = doGraphQLAt[map[string]any](context.Background(), c, c.endpoint, graphQLRequest{OperationName: "CreateCSRFToken"}, nil)
	if hits.Load() != 2 || n.Load() != 1 {
		t.Errorf("observe: hits=%d counted=%d, want sent and counted", hits.Load(), n.Load())
	}
}

// The monitor's own calls carry the monitor's permit once the owner guard has allowed them, and a refused call carries nothing.
func TestOwnerGuardMarksTheCallItAllows(t *testing.T) {
	tok := fence.Token{Group: "g", Pod: "p", Epoch: 1}
	marked := func(ctx context.Context) bool { _, ok := ctx.Value(permitKey{}).(string); return ok }
	for name, src := range map[string]fence.Source{
		"no fence":     nil,
		"lead off":     deadlineFence{fakeFence: fakeFence{st: fence.Off}},
		"held":         deadlineFence{fakeFence{tok: tok, st: fence.Held}, time.Now().Add(time.Minute), true},
		"held, no ddl": fakeFence{tok: tok, st: fence.Held},
	} {
		m := NewStateMonitor(nil, nil)
		m.SetFence(src, func(string) {})
		ctx, cancel, ok := m.rivianCtx(context.Background(), "veh", CallSiteSeed)
		cancel()
		if !ok || !marked(ctx) {
			t.Errorf("%s: ok=%v marked=%v, want an allowed call carrying the monitor's permit", name, ok, marked(ctx))
		}
	}
	m := NewStateMonitor(nil, nil)
	m.SetFence(deadlineFence{fakeFence{st: fence.Lost}, time.Now().Add(time.Minute), true}, func(string) {})
	if ctx, _, ok := m.rivianCtx(context.Background(), "veh", CallSiteSeed); ok || marked(ctx) {
		t.Error("a refused call must not carry a permit")
	}
}

// A monitor call that does not go through rivianCtx carries no permit, however it was started: RefreshVehicleInfo (which asks Rivian for the
// vehicle list from whatever context its caller has) is counted in observe mode and still sent, so the path shows up in the evidence the
// counter exists to collect. The same call after the owner guard allowed it (rivianCtx's context) is not counted.
func TestMonitorCallOutsideTheOwnerGuardIsCountedUnpermitted(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer server.Close()
	n := withMode(t, PermitObserve)
	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.Restore(Session{CSRFToken: "fake-csrf", AppSessionToken: "fake-app", UserSessionToken: "fake-user", AuthenticatedAt: time.Now()})
	m := NewStateMonitor(c, nil)

	_ = m.RefreshVehicleInfo(context.Background()) // errors on the empty answer; the request, and the count, are what matter
	if n.Load() == 0 || hits.Load() == 0 {
		t.Fatalf("an unmarked monitor call: counted=%d sent=%d, want it counted and still sent in observe mode", n.Load(), hits.Load())
	}

	before := n.Load()
	m.SetFence(deadlineFence{fakeFence{tok: fence.Token{Group: "g", Pod: "p", Epoch: 1}, st: fence.Held}, time.Now().Add(time.Minute), true}, func(string) {})
	ctx, cancel, ok := m.rivianCtx(context.Background(), "veh", CallSiteRefreshOnce)
	defer cancel()
	if !ok {
		t.Fatal("the guard refused a held group")
	}
	_ = m.RefreshVehicleInfo(ctx)
	if n.Load() != before {
		t.Errorf("a call under the guard's context was counted (%d new)", n.Load()-before)
	}
}

// The sleep-gate subtree's calls (the extra subscriptions, the schedule read, the start-up reads) are gated and marked at their own call
// sites (M4-1c): a held group gives a marked context with no lease deadline (the client reconnects inside it), a lost one gives nothing.
func TestPermitCtxGatesAndMarksLongLivedCalls(t *testing.T) {
	marked := func(ctx context.Context) bool { _, ok := ctx.Value(permitKey{}).(string); return ok }
	m := NewStateMonitor(nil, nil)
	m.SetFence(deadlineFence{fakeFence{tok: fence.Token{Group: "g", Pod: "p", Epoch: 1}, st: fence.Held}, time.Now().Add(time.Minute), true}, func(string) {})
	ctx, ok := m.permitCtx(context.Background(), "veh", CallSiteSubscription)
	if _, has := ctx.Deadline(); !ok || !marked(ctx) || has {
		t.Errorf("held: ok=%v marked=%v deadline=%v, want an allowed, marked context with no deadline", ok, marked(ctx), has)
	}
	m.SetFence(deadlineFence{fakeFence{st: fence.Lost}, time.Now().Add(time.Minute), true}, func(string) {})
	if ctx, ok := m.permitCtx(context.Background(), "veh", CallSiteSubscription); ok || marked(ctx) {
		t.Errorf("lost: ok=%v marked=%v, want refused and unmarked", ok, marked(ctx))
	}
	wctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, ok := m.permitCtxWait(wctx, "veh", CallSiteSubscription); ok {
		t.Error("permitCtxWait returned ok for a group that is not ours")
	}
}

// The charging-schedule read goes out under the guard's permit: in observe mode it is not counted, and when the group is not ours it is not sent.
func TestScheduleReadCarriesThePermit(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer server.Close()
	n := withMode(t, PermitObserve)
	c := NewLive()
	c.endpoint = server.URL + "/gateway"
	c.Restore(Session{CSRFToken: "fake-csrf", AppSessionToken: "fake-app", UserSessionToken: "fake-user", AuthenticatedAt: time.Now()})
	m := NewStateMonitor(c, nil)
	m.SetFence(deadlineFence{fakeFence{tok: fence.Token{Group: "g", Pod: "p", Epoch: 1}, st: fence.Held}, time.Now().Add(time.Minute), true}, func(string) {})
	m.readChargingSchedules(context.Background(), "veh", 0)
	if hits.Load() == 0 || n.Load() != 0 {
		t.Fatalf("held: sent=%d unpermitted=%d, want the read sent and not counted", hits.Load(), n.Load())
	}
	sent := hits.Load()
	m.SetFence(deadlineFence{fakeFence{st: fence.Lost}, time.Now().Add(time.Minute), true}, func(string) {})
	m.readChargingSchedules(context.Background(), "veh", 0)
	if hits.Load() != sent {
		t.Error("the schedule was read for a group this pod does not hold")
	}
}
