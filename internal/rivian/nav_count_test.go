package rivian

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// navCounter is a SleepObserver that also counts navigation frames.
type navCounter struct {
	mu     sync.Mutex
	frames map[string]int
	bytes  map[string]int
}

func (n *navCounter) OnSleepEnded(string, time.Duration, bool) {}

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
