package rivian

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// navCounter is a SleepObserver that also counts navigation frames.
type navCounter struct {
	mu     sync.Mutex
	frames map[string]int
	bytes  map[string]int

	rejected int
}

func (n *navCounter) OnSleepEnded(string, time.Duration, bool) {}

func (n *navCounter) OnNavRejected() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.rejected++
}

func (n *navCounter) OnNavFrame(topic string, payloadBytes int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.frames[topic]++
	n.bytes[topic] += payloadBytes
}

// A destination is a precise location: the two navigation topics are
// subscribed only with RIVOLT_NAV_COUNT, and not when wake safety turned
// the extra topics off.
func TestNavTopicsOnlyWhenFlagOnAndCaptureAllowed(t *testing.T) {
	pxNextCaptureUntil = time.Time{}
	m := &StateMonitor{logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))}
	if got := m.captureTopics("veh-1")(); got != nil {
		t.Errorf("flag off: %v, want none", got)
	}
	m.navCount = true
	if got := m.captureTopics("veh-1")(); !slices.Equal(got, NavCountRVMs) {
		t.Errorf("flag on, capture off: %v, want the nav pair", got)
	}
	m.parallaxCapture = true
	got := m.captureTopics("veh-1")()
	for _, want := range NavCountRVMs {
		if !slices.Contains(got, want) {
			t.Errorf("flag on with capture: %v missing %s", got, want)
		}
	}
	m.SetWakeSafety(func(string) WakeSafety { return WakeSafety{CaptureOff: true} })
	if got := m.captureTopics("veh-1")(); got != nil {
		t.Errorf("wake-safety capture off: %v, want none", got)
	}
}

// The frame builder sizes a navigation payload and carries nothing from it:
// no value, no raw payload, no timestamp.
func TestNavFrameCarriesOnlySize(t *testing.T) {
	raw := []byte("not-a-real-destination-0123456789")
	b64 := base64.StdEncoding.EncodeToString(raw)
	for _, rvm := range NavCountRVMs {
		f := driveDynamicsFrameOf(rvm, b64, `"1700000000000"`)
		want := DriveDynamicsFrame{RVM: rvm, PayloadBytes: len(raw)}
		if f != want {
			t.Errorf("%s: frame %+v, want %+v", rvm, f, want)
		}
	}
	// Any other topic still decodes as before.
	if f := driveDynamicsFrameOf(rvmDriveGear, base64.StdEncoding.EncodeToString([]byte{0x08, 0x03}), `"5"`); !f.ValueOK || f.Payload == "" || f.TimestampMs != 5 {
		t.Errorf("non-nav frame changed: %+v", f)
	}
}

// A navigation frame reaches the counter and nothing else: not the cache,
// not the Parallax liveness bookkeeping, not the log.
func TestNavFrameIsCountedAndNothingElse(t *testing.T) {
	logs := &bytes.Buffer{}
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	obs := &navCounter{frames: map[string]int{}, bytes: map[string]int{}}
	m.SetSleepObserver(obs)

	const secret = "SECRET-DESTINATION-PAYLOAD"
	for _, f := range []DriveDynamicsFrame{
		// Fields a real frame never has, to prove none is read or logged
		// even if one were present.
		{RVM: rvmNavTripInfo, PayloadBytes: 120, Payload: secret, Value: 7, ValueOK: true, TimestampMs: 1},
		{RVM: rvmNavTripProgress, PayloadBytes: 30, Payload: secret},
		{RVM: rvmNavTripProgress, PayloadBytes: 31},
	} {
		m.onDriveDynamicsFrame(context.Background(), "veh-1", f)
	}

	if obs.frames[rvmNavTripInfo] != 1 || obs.frames[rvmNavTripProgress] != 2 {
		t.Errorf("frames %v", obs.frames)
	}
	if obs.bytes[rvmNavTripInfo] != 120 || obs.bytes[rvmNavTripProgress] != 61 {
		t.Errorf("bytes %v", obs.bytes)
	}
	if strings.Contains(logs.String(), secret) || logs.Len() != 0 {
		t.Errorf("a navigation frame logged: %q", logs.String())
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.cache) != 0 {
		t.Errorf("cache changed: %v", m.cache)
	}
	if len(m.lastParallaxAt) != 0 {
		t.Errorf("parallax liveness touched: %v", m.lastParallaxAt)
	}
}

// If the server refuses a subscription that carried the navigation topics
// (a gate, say), the reconnect carries the core topics without them, the
// refusal is counted and logged once, and the car's other Parallax topics
// keep flowing.
func TestNavTopicsLatchOffWhenTheServerRefusesThem(t *testing.T) {
	type sub struct{ rvms []string }
	var mu sync.Mutex
	var subs []sub
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"graphql-transport-ws"}})
		if err != nil {
			return
		}
		ctx := r.Context()
		if _, _, err := c.Read(ctx); err != nil { // connection_init
			return
		}
		_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"connection_ack"}`))
		for {
			_, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			var f struct {
				ID, Type string
				Payload  struct {
					Variables struct{ RVMs []string }
				}
			}
			if json.Unmarshal(data, &f) != nil || f.Type != "subscribe" {
				continue
			}
			mu.Lock()
			subs = append(subs, sub{rvms: f.Payload.Variables.RVMs})
			n := len(subs)
			mu.Unlock()
			if n == 1 {
				_ = c.Write(ctx, websocket.MessageText, []byte(`{"id":"`+f.ID+`","type":"error","payload":[{"message":"not allowed"}]}`))
				continue
			}
			_ = c.Write(ctx, websocket.MessageText, []byte(`{"id":"`+f.ID+`","type":"next","payload":{"data":{"parallaxMessages":{"payload":"CAM=","timestamp":"5","rvm":"`+rvmDriveGear+`"}}}}`))
		}
	}))
	defer srv.Close()

	c := NewLive()
	c.userSessionToken = "u"
	c.wsEndpointOverride = "ws" + strings.TrimPrefix(srv.URL, "http")
	logs := &bytes.Buffer{}
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(logs, nil)))
	m.navCount = true
	obs := &navCounter{frames: map[string]int{}, bytes: map[string]int{}}
	m.SetSleepObserver(obs)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	gotGear := make(chan struct{})
	var once sync.Once
	go func() {
		_ = c.SubscribeDriveDynamics(ctx, "veh-1", m.captureTopics("veh-1"), func(f DriveDynamicsFrame) {
			m.onDriveDynamicsFrame(ctx, "veh-1", f)
			if f.RVM == rvmDriveGear {
				once.Do(func() { close(gotGear) })
			}
		})
	}()
	select {
	case <-gotGear:
	case <-ctx.Done():
		t.Fatal("no gear frame after the reconnect")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(subs) < 2 {
		t.Fatalf("%d subscriptions, want a reconnect", len(subs))
	}
	has := func(rvms []string, want string) bool { return slices.Contains(rvms, want) }
	if !has(subs[0].rvms, rvmNavTripInfo) || !has(subs[0].rvms, rvmNavTripProgress) {
		t.Errorf("first connect lacked the nav topics: %v", subs[0].rvms)
	}
	if has(subs[1].rvms, rvmNavTripInfo) || has(subs[1].rvms, rvmNavTripProgress) {
		t.Errorf("reconnect still carried a nav topic: %v", subs[1].rvms)
	}
	if !has(subs[1].rvms, rvmDriveGear) || !has(subs[1].rvms, rvmChargingStatus) {
		t.Errorf("reconnect lost the core topics: %v", subs[1].rvms)
	}
	obs.mu.Lock()
	defer obs.mu.Unlock()
	if obs.rejected != 1 {
		t.Errorf("rejection counted %d times, want 1", obs.rejected)
	}
	if n := strings.Count(logs.String(), "server refused the navigation topics"); n != 1 {
		t.Errorf("logged %d times, want once:\n%s", n, logs.String())
	}
}
