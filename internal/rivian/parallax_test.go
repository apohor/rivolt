package rivian

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"testing"
)

// fixed32Field encodes a protobuf fixed32 (wire type 5) float field.
func fixed32Field(field int, v float32) []byte {
	b := []byte{byte(field<<3 | 5)}
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], math.Float32bits(v))
	return append(b, buf[:]...)
}

// lenDelimField encodes a protobuf length-delimited (wire type 2) field.
func lenDelimField(field int, body []byte) []byte {
	out := []byte{byte(field<<3 | 2)}
	var l [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(l[:], uint64(len(body)))
	out = append(out, l[:n]...)
	return append(out, body...)
}

// TestDecodeBatteryTemp builds a battery_state protobuf the way Rivian's
// Parallax channel frames it (temperature_state = field 2; avg/max/min =
// fields 1/2/3), base64s it, and checks the decoder pulls the right
// cell temperatures out. Values mirror the RivDocs sample (~41/45/36).
func TestDecodeBatteryTemp(t *testing.T) {
	// temperature_state { avg=41, max=45, min=36 }
	temperatureState := append(append(
		fixed32Field(1, 41.0),
		fixed32Field(2, 45.0)...),
		fixed32Field(3, 36.0)...)
	// battery_state { charge_state(1)=..., temperature_state(2)=... }
	// include a decoy field 1 (charge_state submessage) to prove we
	// select field 2 specifically, not "the first message".
	batteryState := append(
		lenDelimField(1, []byte{0x08, 0x3a}), // charge_state stub
		lenDelimField(2, temperatureState)...,
	)
	b64 := base64.StdEncoding.EncodeToString(batteryState)

	bt, err := decodeBatteryTemp(b64)
	if err != nil {
		t.Fatalf("decodeBatteryTemp: %v", err)
	}
	if bt.CellAvgC != 41 || bt.CellMaxC != 45 || bt.CellMinC != 36 {
		t.Fatalf("got avg=%v max=%v min=%v, want 41/45/36", bt.CellAvgC, bt.CellMaxC, bt.CellMinC)
	}
}

// TestDecodeBatteryTemp_NoTemperatureState errors clearly when the
// battery_state frame carries no temperature_state (e.g. a partial
// update before the pack reports).
func TestDecodeBatteryTemp_NoTemperatureState(t *testing.T) {
	// battery_state with only charge_state (field 1).
	onlyCharge := lenDelimField(1, []byte{0x08, 0x3a})
	b64 := base64.StdEncoding.EncodeToString(onlyCharge)
	if _, err := decodeBatteryTemp(b64); err == nil {
		t.Fatal("expected an error when temperature_state is absent")
	}
}

// TestDecodeDynamicsGNSS decodes a real dynamics.vehicle.gnss frame
// captured from a parked R1S on preview. Pins the field mapping
// (lat=1, lon=2, alt=3 doubles; heading=5 float; ts=10 varint; speed=4
// absent while parked). Guards against a wire-layout regression.
func TestDecodeDynamicsGNSS(t *testing.T) {
	const frame = "CQAAAADMjT5AEQAAAADIcFjAGQAAAADMDG5ALWdmrEM1zcwMQD1nZuY/Rc3MzD1NZ2ZmP1Cc/e/D9DM="
	g, err := decodeDynamicsGNSS(frame)
	if err != nil {
		t.Fatalf("decodeDynamicsGNSS: %v", err)
	}
	near := func(got, want, tol float64) bool { return got-want < tol && want-got < tol }
	if !near(g.Latitude, 30.55389, 1e-4) {
		t.Errorf("lat = %v, want ~30.55389", g.Latitude)
	}
	if !near(g.Longitude, -97.76221, 1e-4) {
		t.Errorf("lon = %v, want ~-97.76221", g.Longitude)
	}
	if !near(g.AltitudeM, 240.4, 0.1) {
		t.Errorf("alt = %v, want ~240.4", g.AltitudeM)
	}
	if !near(g.HeadingDeg, 344.8, 0.1) {
		t.Errorf("heading = %v, want ~344.8", g.HeadingDeg)
	}
	if g.TimestampMs != 1783627513500 {
		t.Errorf("ts = %d, want 1783627513500", g.TimestampMs)
	}
	// Parked capture: speed omitted → decodes to 0.
	if g.SpeedMS != 0 {
		t.Errorf("speed = %v, want 0 (parked)", g.SpeedMS)
	}
}

// TestDecodeBatteryTemp_PartialTemps tolerates protobuf's omit-zero
// behaviour: a frame that only carries avg (min/max omitted) decodes
// avg and leaves the others at zero.
func TestDecodeBatteryTemp_PartialTemps(t *testing.T) {
	ts := fixed32Field(1, 40.5) // only avg
	bs := lenDelimField(2, ts)
	bt, err := decodeBatteryTemp(base64.StdEncoding.EncodeToString(bs))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bt.CellAvgC != 40.5 || bt.CellMaxC != 0 || bt.CellMinC != 0 {
		t.Fatalf("got avg=%v max=%v min=%v, want 40.5/0/0", bt.CellAvgC, bt.CellMaxC, bt.CellMinC)
	}
}

// TestDecodeSingleVarint pins the drive-dynamics wire shapes against the
// real frames captured from a parked R1S via /api/parallax-raw
// (2026-07-13). All three topics are { field 1: varint }. The odometer
// value (60355) cross-checks to 37502.9 mi vs vehicleState's 37503.06 mi
// at capture — confirming whole-kilometer units.
func TestDecodeSingleVarint(t *testing.T) {
	cases := []struct {
		name string
		b64  string
		want uint64
	}{
		{"gear_park", "CAE=", 1},        // 08 01
		{"drive_mode", "CAI=", 2},       // 08 02
		{"odometer_km", "CMPXAw==", 60355}, // 08 c3 d7 03
	}
	for _, tc := range cases {
		got, ok := decodeSingleVarint(tc.b64)
		if !ok || got != tc.want {
			t.Errorf("%s: decodeSingleVarint(%q) = %d, %v; want %d, true", tc.name, tc.b64, got, ok, tc.want)
		}
	}
	// Extra trailing field must not break the field-1 read (forward-compat).
	if got, ok := decodeSingleVarint(base64.StdEncoding.EncodeToString([]byte{0x08, 0x04, 0x10, 0x2a})); !ok || got != 4 {
		t.Errorf("trailing field: got %d, %v; want 4, true", got, ok)
	}
	// Malformed / empty payloads report ok=false rather than panicking.
	if _, ok := decodeSingleVarint("!!!!"); ok {
		t.Error("malformed base64 should return ok=false")
	}
	if gearFromParallax(1) != "P" {
		t.Errorf("gearFromParallax(1) = %q, want P", gearFromParallax(1))
	}
	if gearFromParallax(99) != "" {
		t.Errorf("gearFromParallax(99) = %q, want empty (unmapped)", gearFromParallax(99))
	}
}

// TestDecodeTires pins the dynamics.tires.state layout against a real frame
// captured 2026-07-14 (FL/FR/RL/RR = 3.25/3.28/3.25/3.25 bar, matching
// vehicleState's tire_min_bar 3.25). Repeated TireState in field 2, each
// with position (1) and pressure double (3).
func TestDecodeTires(t *testing.T) {
	const frame = "CAESFAgBEAEZAAAAAAAACkAo4JT17/UzEhQIAhABGT0K16NwPQpAKOCU9e/1MxIUCAMQARkAAAAAAAAKQCjglPXv9TMSFAgEEAEZAAAAAAAACkAo4JT17/Uz"
	got, ok := decodeTires(frame)
	if !ok || len(got) != 4 {
		t.Fatalf("decodeTires: ok=%v len=%d, want ok=true len=4", ok, len(got))
	}
	want := map[int]float64{1: 3.25, 2: 3.28, 3: 3.25, 4: 3.25}
	for _, tp := range got {
		w, exists := want[tp.Position]
		if !exists {
			t.Errorf("unexpected position %d", tp.Position)
			continue
		}
		if d := tp.Bar - w; d > 0.01 || d < -0.01 {
			t.Errorf("position %d: bar=%.3f, want ~%.2f", tp.Position, tp.Bar, w)
		}
	}
	// range shares the single-varint shape: field 1 = 308 km.
	if v, ok := decodeSingleVarint("CLQCEAEYAQ=="); !ok || v != 308 {
		t.Errorf("range decodeSingleVarint = %d, %v; want 308, true", v, ok)
	}
}
