package rivian

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/apohor/rivolt/internal/drives"
)

// --- fixture encoder: builds trip_info / trip_progress from the field table,
// with made-up coordinates (none near anyone's home). ---

func fxVarint(field int, v uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(field<<3)), v)
}

func fxDouble(field int, f float64) []byte {
	b := binary.AppendUvarint(nil, uint64(field<<3|1))
	return binary.LittleEndian.AppendUint64(b, math.Float64bits(f))
}

func fxBytes(field int, b []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3|2))
	out = binary.AppendUvarint(out, uint64(len(b)))
	return append(out, b...)
}

func fxCat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func fxCoord(lat, lon float64) []byte { return fxCat(fxDouble(1, lat), fxDouble(2, lon)) }

func fxWaypoint(name string, lat, lon float64) []byte {
	return fxBytes(1, fxCat(fxBytes(1, fxCoord(lat, lon)), fxVarint(3, 1), fxBytes(4, []byte(name))))
}

func fxChargeStop(name string, lat, lon float64) []byte {
	return fxBytes(2, fxCat(fxBytes(2, []byte(name)), fxBytes(3, fxCoord(lat, lon))))
}

func fxLeg(metres float64) []byte {
	return fxBytes(4, fxCat(fxDouble(1, metres), fxDouble(2, 600), fxBytes(4, []byte("LEGLINE"))))
}

func fxTripInfo(route ...[]byte) []byte {
	return fxCat(fxBytes(1, []byte("trip-1")), fxBytes(2, fxBytes(1, fxCoord(1, 1))), fxBytes(3, fxCat(route...)), fxVarint(5, 80))
}

func fxStops(stops ...[]byte) [][]byte {
	out := make([][]byte, len(stops))
	for i, s := range stops {
		out[i] = fxBytes(3, s)
	}
	return out
}

func fxProgress(eta time.Time, next int, legMetres float64) []byte {
	return fxCat(
		fxBytes(1, fxVarint(1, uint64(eta.Unix()-60))),
		fxBytes(2, fxVarint(1, uint64(eta.Unix()))),
		fxVarint(3, uint64(next)),
		fxDouble(4, legMetres), fxDouble(5, 300),
		fxBytes(6, fxBytes(1, fxCoord(40, -100))),
	)
}

func fxB64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

var navNow = time.Unix(1_800_000_000, 0)

func TestDecodeNavInfoKeepsAndDrops(t *testing.T) {
	route := fxCat(append(append(fxStops(fxWaypoint("Via", 41, -101), fxWaypoint("Cafe Nowhere", 42.5, -102.25)), fxLeg(1500), fxLeg(2500)),
		fxBytes(10, []byte("POLYLINE")), fxBytes(11, []byte("ALTERNATIVE")))...)
	f, res := decodeNavFrame(rvmNavTripInfo, fxB64(fxTripInfo(route)), navNow)
	if res != NavResultOK || f == nil || f.Info == nil {
		t.Fatalf("result %q frame %v", res, f)
	}
	want := NavInfo{Name: "Cafe Nowhere", Lat: 42.5, Lon: -102.25, Stops: 2, LegMetres: []float64{1500, 2500}, Route: "POLYLINE"}
	if !reflect.DeepEqual(*f.Info, want) {
		t.Errorf("info %+v, want %+v", *f.Info, want)
	}
}

func TestDecodeNavInfoChargeStopDestination(t *testing.T) {
	route := fxCat(fxStops(fxChargeStop("Fast Charger", 33, -99))...)
	f, res := decodeNavFrame(rvmNavTripInfo, fxB64(fxTripInfo(route)), navNow)
	if res != NavResultOK || f.Info.Name != "Fast Charger" || f.Info.Lat != 33 || f.Info.Lon != -99 {
		t.Fatalf("result %q info %+v", res, f)
	}
}

func TestDecodeNavInfoNoStopsIsEmptyTrip(t *testing.T) {
	f, res := decodeNavFrame(rvmNavTripInfo, fxB64(fxTripInfo(fxBytes(10, []byte("LINE")))), navNow)
	if res != NavResultOK || f.Info == nil || f.Info.Stops != 0 {
		t.Fatalf("result %q frame %+v", res, f)
	}
}

func TestDecodeNavProgress(t *testing.T) {
	eta := navNow.Add(time.Hour)
	f, res := decodeNavFrame(rvmNavTripProgress, fxB64(fxProgress(eta, 1, 4321)), navNow)
	if res != NavResultOK || f.Progress == nil {
		t.Fatalf("result %q", res)
	}
	if !f.Progress.ETA.Equal(time.Unix(eta.Unix(), 0)) || f.Progress.NextStop != 1 || f.Progress.LegMetres != 4321 {
		t.Errorf("progress %+v", *f.Progress)
	}
}

func TestDecodeNavLimitsMalformedAndRange(t *testing.T) {
	good := fxWaypoint("Ok", 40, -100)
	long := strings.Repeat("n", navMaxNameBytes+1)
	manyStops := make([][]byte, navMaxStops+1)
	for i := range manyStops {
		manyStops[i] = good
	}
	cases := []struct {
		name, rvm string
		payload   []byte
		want      string
	}{
		{"name too long", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(fxWaypoint(long, 40, -100))...)), NavResultSize},
		{"too many stops", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(manyStops...)...)), NavResultSize},
		{"route too big", rvmNavTripInfo, fxTripInfo(fxCat(append(fxStops(good), fxBytes(10, bytes.Repeat([]byte("p"), navMaxRouteBytes+1)))...)), NavResultSize},
		{"frame too big", rvmNavTripInfo, bytes.Repeat([]byte{0}, navMaxFrameBytes+1), NavResultSize},
		{"truncated", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(good)...))[:20], NavResultMalformed},
		{"bad utf8", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(fxBytes(1, fxCat(fxBytes(1, fxCoord(40, -100)), fxBytes(4, []byte{0xff, 0xfe}))))...)), NavResultMalformed},
		{"lat out of range", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(fxWaypoint("x", 91, 0))...)), NavResultRange},
		{"null island", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(fxWaypoint("x", 0, 0))...)), NavResultRange},
		{"no coordinate", rvmNavTripInfo, fxTripInfo(fxCat(fxStops(fxBytes(1, fxBytes(4, []byte("x"))))...)), NavResultRange},
		{"nan leg", rvmNavTripInfo, fxTripInfo(fxCat(append(fxStops(good), fxBytes(4, fxDouble(1, math.NaN())))...)), NavResultRange},
		{"eta past", rvmNavTripProgress, fxProgress(navNow.Add(-time.Hour), 0, 10), NavResultRange},
		{"eta far future", rvmNavTripProgress, fxProgress(navNow.Add(72*time.Hour), 0, 10), NavResultRange},
		{"negative distance", rvmNavTripProgress, fxProgress(navNow.Add(time.Hour), 0, -1), NavResultRange},
		{"progress truncated", rvmNavTripProgress, fxProgress(navNow.Add(time.Hour), 0, 10)[:9], NavResultMalformed},
	}
	for _, c := range cases {
		f, res := decodeNavFrame(c.rvm, fxB64(c.payload), navNow)
		if res != c.want || f != nil {
			t.Errorf("%s: result %q (frame %v), want %q dropped", c.name, res, f, c.want)
		}
	}
	if _, res := decodeNavFrame(rvmNavTripInfo, "!!!not base64", navNow); res != NavResultMalformed {
		t.Errorf("bad base64: %q", res)
	}
}

// Unknown fields, deep nesting and random bytes never panic or decode.
func TestDecodeNavSurvivesJunk(t *testing.T) {
	deep := []byte{0x08, 0x01}
	for i := 0; i < 2000; i++ {
		deep = fxBytes(3, deep)
	}
	for _, p := range [][]byte{deep, {0xff}, {0x1a, 0xff, 0xff, 0xff, 0x7f}, fxCat(fxVarint(99, 5), fxBytes(98, []byte("x")))} {
		for _, rvm := range NavCountRVMs {
			if f, res := decodeNavFrame(rvm, fxB64(p), navNow); res == NavResultOK && f != nil && f.Info != nil && f.Info.Stops > 0 {
				t.Errorf("junk decoded to a trip: %+v", f.Info)
			}
		}
	}
	seed := uint32(1)
	for i := 0; i < 500; i++ {
		buf := make([]byte, 1+i%64)
		for j := range buf {
			seed = seed*1664525 + 1013904223
			buf[j] = byte(seed >> 24)
		}
		for _, rvm := range NavCountRVMs {
			decodeNavFrame(rvm, fxB64(buf), navNow)
		}
	}
}

// Golden bytes pin the fixture encoder against the field table.
func TestNavFixtureGolden(t *testing.T) {
	got := fmt.Sprintf("%x", fxProgress(time.Unix(1_800_003_600, 0), 1, 100))
	const want = "0a0608d4bfa7da0612060890c0a7da061801210000000000005940290000000000c0724032140a120900000000000044401100000000000059c0"
	if got != want {
		t.Errorf("fixture bytes %s, want %s", got, want)
	}
}

func TestNavStoreActiveTripAndMilesLeft(t *testing.T) {
	clock := navNow
	s := newNavStore(func() time.Time { return clock })
	var cleared []string
	s.onClear = func(r string) { cleared = append(cleared, r) }
	info := &NavInfo{Name: "Dest", Lat: 40, Lon: -100, Stops: 3, LegMetres: []float64{1000, 2000, 4000}, Route: "R"}
	s.apply("v", &NavFrame{Progress: &NavProgress{ETA: navNow.Add(time.Hour), NextStop: 0, LegMetres: 5}})
	if _, ok := s.get("v"); ok {
		t.Fatal("progress without a route is a trip")
	}
	s.apply("v", &NavFrame{Info: info})
	if _, ok := s.get("v"); ok {
		t.Fatal("route without progress is a trip")
	}
	s.apply("v", &NavFrame{Progress: &NavProgress{ETA: navNow.Add(time.Hour), NextStop: 1, LegMetres: 500}})
	trip, ok := s.get("v")
	if !ok || trip.Name != "Dest" || trip.MetresLeft != 4500 || trip.Route != "R" {
		t.Fatalf("trip %+v ok=%v", trip, ok)
	}
	// stale
	clock = clock.Add(navStaleFloor + time.Second)
	if _, ok := s.get("v"); ok || !reflect.DeepEqual(cleared, []string{NavClearStale}) {
		t.Fatalf("stale: ok=%v cleared=%v", ok, cleared)
	}
}

func TestNavStoreClearRules(t *testing.T) {
	s := newNavStore(func() time.Time { return navNow })
	var cleared []string
	s.onClear = func(r string) { cleared = append(cleared, r) }
	info := &NavInfo{Name: "A", Lat: 40, Lon: -100, Stops: 1, LegMetres: []float64{10}}
	prog := &NavProgress{ETA: navNow.Add(time.Hour), LegMetres: 1}
	start := func() { s.apply("v", &NavFrame{Info: info}); s.apply("v", &NavFrame{Progress: prog}) }

	start()
	s.apply("v", &NavFrame{Info: &NavInfo{}}) // empty stop list
	start()
	s.apply("v", &NavFrame{Progress: &NavProgress{ETA: prog.ETA, NextStop: 1}}) // next stop left the list
	start()
	s.clear("v", NavClearDrive)
	start()
	s.clearAll(NavClearReconnect)
	start()
	s.apply("v", &NavFrame{Info: &NavInfo{Name: "B", Lat: 41, Lon: -101, Stops: 1}}) // new destination drops progress
	if _, ok := s.get("v"); ok {
		t.Error("a new destination kept the old trip's progress")
	}
	s.clear("none", NavClearDrive) // nothing held: no count
	want := []string{NavClearEnded, NavClearEnded, NavClearDrive, NavClearReconnect, NavClearNewTrip}
	if !reflect.DeepEqual(cleared, want) {
		t.Errorf("cleared %v, want %v", cleared, want)
	}
}

// --- monitor integration ---

type navRecorder struct {
	navCounter
	results map[string]int
	clears  map[string]int
}

func newNavRecorder() *navRecorder {
	return &navRecorder{navCounter: navCounter{frames: map[string]int{}, bytes: map[string]int{}}, results: map[string]int{}, clears: map[string]int{}}
}

func (n *navRecorder) OnNavDecode(topic, result string) {
	n.mu.Lock()
	n.results[topic+"/"+result]++
	n.mu.Unlock()
}

func (n *navRecorder) OnNavCleared(reason string) {
	n.mu.Lock()
	n.clears[reason]++
	n.mu.Unlock()
}

func navFrames(name string, eta time.Time, decode bool) (info, prog DriveDynamicsFrame) {
	route := fxCat(append(fxStops(fxWaypoint(name, 42.5, -102.25)), fxLeg(3000), fxBytes(10, []byte("POLYLINE")))...)
	info = driveDynamicsFrameOf(rvmNavTripInfo, fxB64(fxTripInfo(route)), `"1"`, decode)
	prog = driveDynamicsFrameOf(rvmNavTripProgress, fxB64(fxProgress(eta, 0, 1200)), `"1"`, decode)
	return
}

func TestMonitorNavFlagOffCountsOnly(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	obs := newNavRecorder()
	m.SetSleepObserver(obs)
	info, prog := navFrames("Nowhere Cafe", time.Now().Add(time.Hour), false)
	if info.Nav != nil || prog.Nav != nil {
		t.Fatal("decoded with the gate off")
	}
	// Even a frame that somehow carried a decoded trip is not folded in with the gate off.
	info.Nav = &NavFrame{Info: &NavInfo{Name: "x", Lat: 1, Lon: 1, Stops: 1}}
	m.onDriveDynamicsFrame(context.Background(), "v", info)
	if _, ok := m.NavDestination("v"); ok {
		t.Error("flag off served a trip")
	}
	if obs.frames[rvmNavTripInfo] != 1 || len(obs.results) != 0 {
		t.Errorf("frames %v results %v", obs.frames, obs.results)
	}
}

func TestMonitorNavFlowAndClears(t *testing.T) {
	m := NewStateMonitor(nil, nil)
	obs := newNavRecorder()
	m.SetSleepObserver(obs)
	var on sync.Map
	on.Store("on", true)
	m.SetNavDecodeGate(func() bool { _, ok := on.Load("on"); return ok })

	feed := func() {
		i, p := navFrames("Nowhere Cafe", time.Now().Add(time.Hour), true)
		m.onDriveDynamicsFrame(context.Background(), "v", i)
		m.onDriveDynamicsFrame(context.Background(), "v", p)
	}
	feed()
	trip, ok := m.NavDestination("v")
	if !ok || trip.Name != "Nowhere Cafe" || trip.MetresLeft != 1200 {
		t.Fatalf("trip %+v ok=%v", trip, ok)
	}
	if obs.results["trip_info/ok"] != 1 || obs.results["trip_progress/ok"] != 1 {
		t.Errorf("results %v", obs.results)
	}
	// reconnect
	m.onDriveDynamicsFrame(context.Background(), "v", DriveDynamicsFrame{NavReset: true})
	if _, ok := m.NavDestination("v"); ok {
		t.Error("trip survived a reconnect")
	}
	// drive close
	feed()
	m.fireDriveCloseHook(drives.Drive{ID: "d1", VehicleID: "v"})
	if _, ok := m.NavDestination("v"); ok {
		t.Error("trip survived the drive closing")
	}
	// flag flipped off clears what is held
	feed()
	on.Delete("on")
	if _, ok := m.NavDestination("v"); ok {
		t.Error("flag off served a trip")
	}
	on.Store("on", true)
	if _, ok := m.NavDestination("v"); ok {
		t.Error("trip came back after the flag toggled")
	}
	for _, r := range []string{NavClearReconnect, NavClearDrive, NavClearFlagOff} {
		if obs.clears[r] != 1 {
			t.Errorf("clears %v, want one %s", obs.clears, r)
		}
	}
}

// The destination text and coordinates reach none of: the logs, the
// monitor's cache and every other field of its state, the metrics the
// observer would emit, or a JSON dump of the cache.
func TestNavSentinelNeverLeaks(t *testing.T) {
	const secret = "SENTINEL-PLACE-NAME-7731"
	logs := &bytes.Buffer{}
	m := NewStateMonitor(nil, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	obs := newNavRecorder()
	m.SetSleepObserver(obs)
	m.SetNavDecodeGate(func() bool { return true })

	info, prog := navFrames(secret, time.Now().Add(time.Hour), true)
	m.onDriveDynamicsFrame(context.Background(), "v", info)
	m.onDriveDynamicsFrame(context.Background(), "v", prog)
	// malformed and out-of-range frames log nothing either
	m.onDriveDynamicsFrame(context.Background(), "v", driveDynamicsFrameOf(rvmNavTripInfo, fxB64(fxTripInfo(fxCat(fxStops(fxWaypoint(secret, 99, 0))...))), `"1"`, true))
	if _, ok := m.NavDestination("v"); !ok {
		t.Fatal("the trip was not held, the test proves nothing")
	}

	m.mu.RLock()
	cacheJSON, _ := json.Marshal(m.cache)
	m.mu.RUnlock()
	dump := strings.Join([]string{logs.String(), string(cacheJSON), fmt.Sprint(obs.frames, obs.bytes, obs.results, obs.clears)}, "\n")
	for _, needle := range []string{secret, "42.5", "-102.25", "POLYLINE"} {
		if strings.Contains(dump, needle) {
			t.Errorf("%q leaked outside the store:\n%s", needle, dump)
		}
	}
	if logs.Len() != 0 {
		t.Errorf("a navigation frame logged: %q", logs.String())
	}
}

// The trip lives in navStore only: no type that is persisted, snapshotted
// or served as state may gain a field that holds a trip.
func TestNavTripIsNotReachableFromStateTypes(t *testing.T) {
	banned := map[reflect.Type]bool{
		reflect.TypeOf(NavTrip{}): true, reflect.TypeOf(NavInfo{}): true, reflect.TypeOf(NavProgress{}): true,
		reflect.TypeOf(NavFrame{}): true, reflect.TypeOf(&navStore{}): true, reflect.TypeOf(navStore{}): true,
	}
	var walk func(t reflect.Type, path string, seen map[reflect.Type]bool)
	walk = func(rt reflect.Type, path string, seen map[reflect.Type]bool) {
		if seen[rt] {
			return
		}
		seen[rt] = true
		if banned[rt] {
			t.Errorf("%s reaches %v", path, rt)
		}
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(rt.Elem(), path, seen)
		case reflect.Map:
			walk(rt.Key(), path, seen)
			walk(rt.Elem(), path, seen)
		case reflect.Struct:
			for i := 0; i < rt.NumField(); i++ {
				walk(rt.Field(i).Type, path+"."+rt.Field(i).Name, seen)
			}
		}
	}
	for _, v := range []any{State{}, LiveDrive{}, ParallaxRawFrame{}} {
		walk(reflect.TypeOf(v), reflect.TypeOf(v).Name(), map[reflect.Type]bool{})
	}
}

func TestProbeParallaxRawRefusesNavTopics(t *testing.T) {
	c := NewLive()
	c.userSessionToken = "u"
	for _, rvms := range [][]string{{rvmNavTripInfo}, {"dynamics.vehicle.gnss", " " + rvmNavTripProgress}} {
		if _, err := c.ProbeParallaxRaw(context.Background(), "veh", rvms, 1); err != ErrNavProbeRefused {
			t.Errorf("%v: err %v, want ErrNavProbeRefused", rvms, err)
		}
	}
}
