package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Battery pack temperature over Parallax.
//
// Rivolt already speaks Parallax for charging (see ws_parallax.go). The
// same subscription carries a separate RVM topic,
// energy.high_voltage.battery_state, whose protobuf payload includes the
// per-pack cell temperatures the legacy vehicleState selection lacks.
//
// Wire shape (3.13.1 APK l70/p, l70/l + RivDocs Parallax energy domain):
//
//	message BatteryState {          // l70/p
//	  ChargeState      charge_state       = 1;
//	  TemperatureState temperature_state  = 2;   // <- target
//	  ThermalEvent     thermal_event      = 3;
//	  ...
//	}
//	message TemperatureState {       // l70/l
//	  float cell_avg_temperature_celsius = 1;
//	  float cell_max_temperature_celsius = 2;
//	  float cell_min_temperature_celsius = 3;
//	}

// rvmBatteryState is the Parallax RVM topic carrying high-voltage pack
// state (SOC, cell temperatures, thermal event).
const rvmBatteryState = "energy.high_voltage.battery_state"

// BatteryTemp holds decoded high-voltage pack cell temperatures (°C).
type BatteryTemp struct {
	CellAvgC  float64 `json:"cell_avg_c"`
	CellMaxC  float64 `json:"cell_max_c"`
	CellMinC  float64 `json:"cell_min_c"`
	Timestamp string  `json:"timestamp"`
	RawB64    string  `json:"raw_b64"`
}

// errParallaxDone stops the subscription loop once the target frame
// has been captured.
var errParallaxDone = errors.New("parallax: got frame")

// ProbeBatteryTemperature opens a short-lived Parallax subscription for
// vehicleID, waits for the first battery_state frame, decodes the pack
// cell temperatures, and returns them. Proof-of-concept: it reuses the
// existing subscription websocket mux + auth, so no new transport.
func (c *LiveClient) ProbeBatteryTemperature(ctx context.Context, vehicleID string) (*BatteryTemp, error) {
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return nil, ErrNotAuthenticated
	}
	if vehicleID == "" {
		return nil, errors.New("rivian: vehicleID is required")
	}

	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var result *BatteryTemp
	err := c.runGenericSubscription(ctx, userTok, subParams{
		operationName: "ParallaxMessages",
		query:         qParallaxMessagesSubscription,
		vehicleID:     vehicleID,
		variables: map[string]any{
			"vehicleId": vehicleID,
			"rvms":      []string{rvmBatteryState},
		},
	}, func(raw json.RawMessage) error {
		var p parallaxNext
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil // tolerate a single bad frame
		}
		if len(p.Errors) > 0 {
			return fmt.Errorf("parallax subscription error: %s", p.Errors[0].Message)
		}
		msg := p.Data.ParallaxMessages
		if msg.RVM != rvmBatteryState || msg.Payload == "" {
			return nil // different RVM slipped through; keep waiting
		}
		bt, derr := decodeBatteryTemp(msg.Payload)
		if derr != nil {
			return fmt.Errorf("decode battery_state: %w", derr)
		}
		bt.Timestamp = strings.Trim(string(msg.Timestamp), `"`)
		bt.RawB64 = msg.Payload
		result = bt
		return errParallaxDone
	})
	if result != nil {
		return result, nil
	}
	if err != nil && !errors.Is(err, errParallaxDone) {
		return nil, err
	}
	return nil, errors.New("parallax: no battery_state frame within timeout (is the vehicle awake?)")
}

// decodeBatteryTemp base64-decodes a battery_state payload and pulls the
// three pack cell temperatures out of its temperature_state submessage.
func decodeBatteryTemp(b64 string) (*BatteryTemp, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(b64); err != nil {
			return nil, fmt.Errorf("base64: %w", err)
		}
	}
	ts, ok, err := pbMessageField(raw, 2) // battery_state.temperature_state
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("no temperature_state (field 2) in battery_state")
	}
	avg, _, err := pbFloatField(ts, 1)
	if err != nil {
		return nil, err
	}
	max, _, err := pbFloatField(ts, 2)
	if err != nil {
		return nil, err
	}
	min, _, err := pbFloatField(ts, 3)
	if err != nil {
		return nil, err
	}
	return &BatteryTemp{CellAvgC: float64(avg), CellMaxC: float64(max), CellMinC: float64(min)}, nil
}

// pbMessageField returns the bytes of the length-delimited (wire type 2)
// field `want`, or ok=false if absent.
func pbMessageField(buf []byte, want int) ([]byte, bool, error) {
	var out []byte
	found := false
	err := pbScan(buf, func(field, wire int, val []byte) {
		if field == want && wire == 2 {
			out = val
			found = true
		}
	})
	return out, found, err
}

// pbFloatField returns the float32 value of a fixed32 (wire type 5)
// field `want`. ok=false when absent (protobuf omits zero-valued
// scalars, so the caller treats absence as 0).
func pbFloatField(buf []byte, want int) (float32, bool, error) {
	var out float32
	found := false
	err := pbScan(buf, func(field, wire int, val []byte) {
		if field == want && wire == 5 && len(val) == 4 {
			out = math.Float32frombits(binary.LittleEndian.Uint32(val))
			found = true
		}
	})
	return out, found, err
}

// pbScan walks the top-level fields of a protobuf buffer, calling fn for
// each with the raw value bytes. Returns an error on a malformed buffer.
func pbScan(buf []byte, fn func(field, wire int, val []byte)) error {
	i := 0
	for i < len(buf) {
		tag, n := binary.Uvarint(buf[i:])
		if n <= 0 {
			return errors.New("protobuf: bad tag varint")
		}
		i += n
		field := int(tag >> 3)
		wire := int(tag & 7)
		switch wire {
		case 0: // varint
			_, m := binary.Uvarint(buf[i:])
			if m <= 0 {
				return errors.New("protobuf: bad varint value")
			}
			fn(field, wire, buf[i:i+m])
			i += m
		case 1: // 64-bit
			if i+8 > len(buf) {
				return errors.New("protobuf: truncated 64-bit")
			}
			fn(field, wire, buf[i:i+8])
			i += 8
		case 2: // length-delimited
			l, m := binary.Uvarint(buf[i:])
			if m <= 0 {
				return errors.New("protobuf: bad length")
			}
			i += m
			if i+int(l) > len(buf) {
				return errors.New("protobuf: truncated message")
			}
			fn(field, wire, buf[i:i+int(l)])
			i += int(l)
		case 5: // 32-bit
			if i+4 > len(buf) {
				return errors.New("protobuf: truncated 32-bit")
			}
			fn(field, wire, buf[i:i+4])
			i += 4
		default:
			return fmt.Errorf("protobuf: unsupported wire type %d", wire)
		}
	}
	return nil
}
