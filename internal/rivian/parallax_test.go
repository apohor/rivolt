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
