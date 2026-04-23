package rivian

import (
	"encoding/binary"
	"math"
	"testing"
)

// TestDecodeChargingLiveData builds a small synthetic
// ChargingSessionLiveData message and asserts the decoder recovers
// the field values. Exercises both wire-5 (float32, the encoding
// used by Rivian in production) and wire-1 (float64, accepted for
// forward-compat). Wire format reference:
// https://protobuf.dev/programming-guides/encoding/
func TestDecodeChargingLiveData(t *testing.T) {
	var buf []byte

	// Field 1 (total_kwh) = 15.6, wire 5 (float32) — matches
	// what Rivian's gateway emits.
	buf = appendTag(buf, 1, 5)
	buf = appendFloat(buf, 15.6)
	// Field 9 (current_power) = 7.4, wire 5.
	buf = appendTag(buf, 9, 5)
	buf = appendFloat(buf, 7.4)
	// Field 2 (pack_kwh) = 15.3 encoded as wire-1 double to
	// verify the forward-compat branch decodes too.
	buf = appendTag(buf, 2, 1)
	buf = appendDouble(buf, 15.3)
	// Field 6 (session_duration_mins) = 129, wire 0 (varint).
	buf = appendTag(buf, 6, 0)
	buf = binary.AppendUvarint(buf, 129)
	// Field 8 (range_added_kms) = 50, wire 0.
	buf = appendTag(buf, 8, 0)
	buf = binary.AppendUvarint(buf, 50)
	// Field 11 (session_cost) = embedded empty message, wire 2 — should be skipped.
	buf = appendTag(buf, 11, 2)
	buf = binary.AppendUvarint(buf, 0)
	// Field 13 (charging_state) = 3 (observed in real traffic), wire 0.
	buf = appendTag(buf, 13, 0)
	buf = binary.AppendUvarint(buf, 3)
	// Field 12 (is_free_session) = true, wire 0.
	buf = appendTag(buf, 12, 0)
	buf = binary.AppendUvarint(buf, 1)

	out, err := decodeChargingLiveData(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if math.Abs(out.TotalKWh-15.6) > 0.001 {
		t.Errorf("TotalKWh=%v want 15.6", out.TotalKWh)
	}
	if math.Abs(out.CurrentPowerKW-7.4) > 0.001 {
		t.Errorf("CurrentPowerKW=%v want 7.4", out.CurrentPowerKW)
	}
	if math.Abs(out.PackKWh-15.3) > 0.001 {
		t.Errorf("PackKWh=%v want 15.3", out.PackKWh)
	}
	if out.SessionDurationMins != 129 {
		t.Errorf("SessionDurationMins=%d want 129", out.SessionDurationMins)
	}
	if out.RangeAddedKms != 50 {
		t.Errorf("RangeAddedKms=%d want 50", out.RangeAddedKms)
	}
	if out.ChargingState != 3 {
		t.Errorf("ChargingState=%d want 3", out.ChargingState)
	}
	if !out.IsFreeSession {
		t.Errorf("IsFreeSession=false want true")
	}
}

func appendTag(buf []byte, fieldNum, wireType int) []byte {
	tag := uint64(fieldNum<<3 | wireType)
	return binary.AppendUvarint(buf, tag)
}

func appendFloat(buf []byte, f float32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], math.Float32bits(f))
	return append(buf, b[:]...)
}

func appendDouble(buf []byte, f float64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(f))
	return append(buf, b[:]...)
}
