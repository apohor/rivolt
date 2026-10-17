package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pbMsg(f int, b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(f<<3|2))
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

func pbVarint(f int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(f<<3)), v)
}

func pbFloat(f int, v float32) []byte {
	return binary.LittleEndian.AppendUint32(binary.AppendUvarint(nil, uint64(f<<3|5)), math.Float32bits(v))
}

func TestPbTree(t *testing.T) {
	inner := append(pbVarint(1, 7), pbMsg(2, []byte("hello"))...)
	raw := append(pbVarint(1, 3), pbFloat(2, 1.5)...)
	raw = append(raw, pbMsg(3, inner)...)
	raw = append(raw, pbMsg(3, inner)...) // repeated
	tree, ok := pbTree(raw, map[int]string{1: "status"}, pxNextSpec{strings: true}, 0)
	if !ok {
		t.Fatal("not a message")
	}
	b, _ := json.Marshal(tree)
	want := `{"1_status":3,"2":1.5,"3":[{"1":7,"2":"hello"},{"1":7,"2":"hello"}]}`
	if string(b) != want {
		t.Errorf("tree = %s\nwant   %s", b, want)
	}
	if _, ok := pbTree([]byte{0xff}, nil, pxNextSpec{}, 0); ok {
		t.Error("garbage parsed as a message")
	}
	long := []byte{}
	for i := 0; i < 40; i++ {
		long = append(long, pbVarint(1, uint64(i))...)
	}
	tree, _ = pbTree(long, nil, pxNextSpec{}, 0)
	if l := tree["1"].([]any); len(l) != pbTreeMaxRepeats+1 || l[pbTreeMaxRepeats] != "…" {
		t.Errorf("a long repeat wasn't cut: %d entries", len(l))
	}
}

// vehicle.network.state carries the Wi-Fi SSID (field 4 → 3): no string of
// that topic may reach the log, nor its raw payload.
func TestPxNextNetworkNeverLogsStrings(t *testing.T) {
	var buf syncBuffer
	m := NewStateMonitor(nil, nil)
	m.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	wifi := append(pbMsg(3, []byte("MyHomeWifi")), pbVarint(7, 80)...)
	cell := append(pbMsg(1, []byte("SomeCarrier")), pbVarint(4, 3)...)
	raw := append(pbVarint(1, 2), pbMsg(4, wifi)...)
	raw = append(raw, pbMsg(5, cell)...)
	b64 := base64.StdEncoding.EncodeToString(raw)
	m.onDriveDynamicsFrame(context.Background(), "v", DriveDynamicsFrame{RVM: "vehicle.network.state", Payload: b64, TimestampMs: time.Now().UnixMilli()})
	l := buf.String()
	if !strings.Contains(l, `"topic":"network"`) || !strings.Contains(l, `"7":80`) {
		t.Fatalf("network frame not decode-logged: %s", l)
	}
	for _, leak := range []string{"MyHomeWifi", "SomeCarrier", b64, "px_raw_b64"} {
		if strings.Contains(l, leak) {
			t.Errorf("log carries %q: %s", leak, l)
		}
	}
}

func TestPxNextTimeBox(t *testing.T) {
	defer func(u time.Time) { pxNextCaptureUntil = u }(pxNextCaptureUntil)
	defer func(u time.Time) { pxChargeCaptureUntil = u }(pxChargeCaptureUntil)
	pxChargeCaptureUntil = time.Time{}
	var buf syncBuffer
	m := NewStateMonitor(nil, nil)
	m.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	m.parallaxCapture = true
	topics := func() map[string]bool {
		out := map[string]bool{}
		for _, t := range m.captureTopics("v")() {
			out[t] = true
		}
		return out
	}
	frame := DriveDynamicsFrame{RVM: "charging.session.soc_slider", Payload: base64.StdEncoding.EncodeToString(pbVarint(1, 80)), TimestampMs: 1}

	pxNextCaptureUntil = time.Now().Add(time.Hour)
	got := topics()
	for _, rvm := range pxNextRVMs {
		if !got[rvm] {
			t.Errorf("%s missing during the capture", rvm)
		}
	}
	m.onDriveDynamicsFrame(context.Background(), "v", frame)
	if !strings.Contains(buf.String(), `"1_user_soc_limit":80`) {
		t.Errorf("soc_slider not logged: %s", buf.String())
	}

	pxNextCaptureUntil = time.Now().Add(-time.Hour)
	if got := topics(); len(got) != len(CaptureRVMs) {
		t.Errorf("after the capture: %d topics, want the %d capture topics", len(got), len(CaptureRVMs))
	}
	buf = syncBuffer{}
	m.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	m.onDriveDynamicsFrame(context.Background(), "v", frame)
	if strings.Contains(buf.String(), "soc_slider") || strings.Contains(buf.String(), "px_raw_b64") {
		t.Errorf("a frame after the capture was logged: %s", buf.String())
	}
}

// The list and the specs agree, nothing overlaps the topics already
// subscribed, and nothing from the skip list (keys, video, Gear Guard
// privacy, profiles, geofences, navigation, commands) is in it.
func TestPxNextTopicSet(t *testing.T) {
	if len(pxNextRVMs) != len(pxNextTopics) {
		t.Errorf("%d RVMs vs %d specs", len(pxNextRVMs), len(pxNextTopics))
	}
	seen := map[string]bool{}
	for _, r := range pxNextRVMs {
		if _, ok := pxNextTopics[r]; !ok || seen[r] {
			t.Errorf("%s: no spec, or listed twice", r)
		}
		seen[r] = true
	}
	for _, r := range append([]string{rvmDriveGear, rvmDriveMode, rvmOdometer, rvmPowerState, rvmRange, rvmTires,
		rvmCabinTemps, rvmClosures, rvmLocks, rvmChargingStatus, rvmOtaDeploy, rvmBatteryChars}, CaptureRVMs...) {
		if seen[r] {
			t.Errorf("%s is already subscribed", r)
		}
	}
	for r := range seen {
		for _, banned := range []string{"gearguard", "passive_entry", "device_table", "secure_file_transfer",
			"holiday_celebration", "profiles", "geofence", "navigation", "remote_command", "streaming"} {
			if strings.Contains(r, banned) {
				t.Errorf("%s is on the skip list", r)
			}
		}
	}
}

// DA: no topic logs strings (opt-in only after a decode proves no free
// text), and the topics that may hold a position round floats to 2 decimals.
func TestPxNextGuardrails(t *testing.T) {
	for rvm, spec := range pxNextTopics {
		if spec.strings {
			t.Errorf("%s logs strings; every topic is numbers-only until a decode proves otherwise", rvm)
		}
	}
	for _, rvm := range []string{"dynamics.vehicle.location", "charging.smart_charging.settings",
		"charging.smart_charging.smart_charging_info", "charging.smart_charging.weighted_charging_forecast"} {
		if !pxNextTopics[rvm].coarse {
			t.Errorf("%s may log a precise position", rvm)
		}
	}
	var buf syncBuffer
	m := NewStateMonitor(nil, nil)
	m.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	coords := append(pbFloat(1, 34.147812), pbFloat(2, -118.144531)...)
	// The same position as E7 integers; a negative int32 is a 10-byte uvarint.
	coords = append(coords, pbVarint(4, 341478120)...)
	neg := int64(-1181445310)
	coords = append(coords, pbVarint(5, uint64(neg))...)
	raw := append(pbVarint(1, 2), pbMsg(2, append(coords, pbMsg(3, []byte("Home sweet home"))...))...)
	m.onDriveDynamicsFrame(context.Background(), "v", DriveDynamicsFrame{RVM: "dynamics.vehicle.location",
		Payload: base64.StdEncoding.EncodeToString(raw), TimestampMs: time.Now().UnixMilli()})
	l := buf.String()
	if !strings.Contains(l, `"1_known_location":2`) {
		t.Fatalf("location not logged: %s", l)
	}
	if !strings.Contains(l, `"4":"big"`) || !strings.Contains(l, `"5":"big"`) {
		t.Errorf("E7 integers not replaced: %s", l)
	}
	for _, leak := range []string{"34.1478", "118.1445", "341478120", "1181445310", strconv.FormatUint(uint64(neg), 10), "Home sweet home"} {
		if strings.Contains(l, leak) {
			t.Errorf("location log carries %q: %s", leak, l)
		}
	}
}
