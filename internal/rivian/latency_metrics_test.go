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
)

type durationRecorder struct {
	mu      sync.Mutex
	results []string
	ops     []string
	ds      []time.Duration
}

func (r *durationRecorder) OnResult(op, class string) {
	r.mu.Lock()
	r.results = append(r.results, op+"/"+class)
	r.mu.Unlock()
}

func (r *durationRecorder) OnDuration(op string, d time.Duration) {
	r.mu.Lock()
	r.ops, r.ds = append(r.ops, op), append(r.ds, d)
	r.mu.Unlock()
}

// A call that reached the gateway reports its duration under its operation, next to its result; a call stopped before the network (here: no permit
// under enforce) reports neither.
func TestRequestDurationIsReportedForCallsThatReachRivian(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"createCsrfToken":{"csrfToken":"c","appSessionToken":"a"}}}`)
	}))
	defer server.Close()
	rec := &durationRecorder{}
	c := NewLive().WithResultObserver(rec)
	c.endpoint = server.URL + "/gateway"

	_, err := doGraphQLAt[map[string]any](context.Background(), c, c.endpoint, graphQLRequest{OperationName: "CreateCSRFToken"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.ops) != 1 || rec.ops[0] != "CreateCSRFToken" || rec.ds[0] < 20*time.Millisecond || rec.ds[0] > 5*time.Second {
		t.Fatalf("durations %v %v, want one for CreateCSRFToken of at least 20 ms", rec.ops, rec.ds)
	}
	if len(rec.results) != 1 {
		t.Errorf("results %v, want one", rec.results)
	}

	// Refused before the network: neither a result nor a duration.
	withMode(t, PermitEnforce)
	_, err = doGraphQLAt[map[string]any](context.Background(), c, c.endpoint, graphQLRequest{OperationName: "CreateCSRFToken"}, nil)
	if !errors.Is(err, ErrNoPermit) || len(rec.ops) != 1 || len(rec.results) != 1 {
		t.Errorf("a call stopped before the network was reported: err=%v durations=%d results=%d", err, len(rec.ops), len(rec.results))
	}
}

// Why a subscription is opened again, in the loop's own order.
func TestWSReconnectReason(t *testing.T) {
	long := wsStaleThreshold
	boom := errors.New("boom")
	for name, tc := range map[string]struct {
		nudged, watchdog bool
		frames           int64
		dur              time.Duration
		err              error
		want             string
	}{
		"a nudge wins":                   {true, true, 0, long, boom, WSReconnectNudge},
		"the watchdog killed it":         {false, true, 5, long, nil, WSReconnectWatchdog},
		"never a frame, a long time":     {false, false, 0, long, nil, WSReconnectNoFrames},
		"no frames but a short session":  {false, false, 0, time.Second, boom, WSReconnectError},
		"an error":                       {false, false, 9, time.Minute, boom, WSReconnectError},
		"returned cleanly":               {false, false, 9, time.Minute, nil, WSReconnectClean},
		"a short clean return, no frame": {false, false, 0, time.Second, nil, WSReconnectClean},
	} {
		if got := wsReconnectReason(tc.nudged, tc.watchdog, tc.frames, tc.dur, tc.err); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	// Every reason the function can return is in the list the counter is created from.
	in := map[string]bool{}
	for _, r := range WSReconnectReasons {
		in[r] = true
	}
	for _, r := range []string{WSReconnectNudge, WSReconnectWatchdog, WSReconnectNoFrames, WSReconnectError, WSReconnectClean} {
		if !in[r] {
			t.Errorf("reason %q missing from WSReconnectReasons", r)
		}
	}
}

func TestWSReconnectObserver(t *testing.T) {
	var got []string
	SetWSReconnectObserver(func(r string) { got = append(got, r) })
	noteWSReconnect(WSReconnectClean)
	SetWSReconnectObserver(nil)
	noteWSReconnect(WSReconnectError) // no observer: nothing happens
	if len(got) != 1 || got[0] != WSReconnectClean {
		t.Errorf("observed %v, want just the clean one", got)
	}
}
