package rivian

import (
	"encoding/binary"
	"math"
	"testing"
)

// TestDecodeChargingLiveData builds a small synthetic
// ChargingSessionLiveData message and asserts the decoder recovers
// the field values. Wire format reference:
// https://protobuf.dev/programming-guides/encoding/
func TestDecodeChargingLiveData(t *testing.T) {
	var buf []byte

	// Field 1 (total_kwh) = 12.5, wire 1 (64-bit).
	buf = appendTag(buf, 1, 1)
	buf = appendDouble(buf, 12.5)
	// Field 9 (current_power) = 7.25, wire 1.
	buf = appendTag(buf, 9, 1)
	buf = appendDouble(buf, 7.25)
	// Field 6 (session_duration_mins) = 42, wire 0 (varint).
	buf = appendTag(buf, 6, 0)
	buf = binary.AppendUvarint(buf, 42)
	// Field 8 (range_added_kms) = 17, wire 0.
	buf = appendTag(buf, 8, 0)
	buf = binary.AppendUvarint(buf, 17)
	// Field 11 (session_cost) = embedded message, wire 2 — should be skipped.
	buf = appendTag(buf, 11, 2)
	buf = binary.AppendUvarint(buf, 3)
	buf = append(buf, 0xaa, 0xbb, 0xcc)
	// Field 13 (charging_state) = 1 (charging), wire 0.
	buf = appendTag(buf, 13, 0)
	buf = binary.AppendUvarint(buf, 1)
	// Field 12 (is_free_session) = true, wire 0.
	buf = appendTag(buf, 12, 0)
	buf = binary.AppendUvarint(buf, 1)

	out, err := decodeChargingLiveData(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TotalKWh != 12.5 {
		t.Errorf("TotalKWh=%v want 12.5", out.TotalKWh)
	}
	if out.CurrentPowerKW != 7.25 {
		t.Errorf("CurrentPowerKW=%v want 7.25", out.CurrentPowerKW)
	}
	if out.SessionDurationMins != 42 {
		t.Errorf("SessionDurationMins=%d want 42", out.SessionDurationMins)
	}
	if out.RangeAddedKms != 17 {
		t.Errorf("RangeAddedKms=%d want 17", out.RangeAddedKms)
	}
	if out.ChargingState != 1 {
		t.Errorf("ChargingState=%d want 1", out.ChargingState)
	}
	if !out.IsFreeSession {
		t.Errorf("IsFreeSession=false want true")
	}
}

func appendTag(buf []byte, fieldNum, wireType int) []byte {
	tag := uint64(fieldNum<<3 | wireType)
	return binary.AppendUvarint(buf, tag)
}

func appendDouble(buf []byte, f float64) []byte {
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], math.Float64bits(f))
	return append(buf, b[:]...)
}
