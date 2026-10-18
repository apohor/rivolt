package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
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
	CellAvgC float64 `json:"cell_avg_c"`
	CellMaxC float64 `json:"cell_max_c"`
	CellMinC float64 `json:"cell_min_c"`
	// SoCPct (charge_state.chargePercentage, field 1.1) and PackKWh
	// (charge_state.pack_capacity, field 1.4) come from the same
	// battery_state message. Zero when the frame omits charge_state.
	SoCPct    float64 `json:"soc_pct"`
	PackKWh   float64 `json:"pack_kwh"`
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
		bt, err := parseBatteryFrame(raw)
		if err != nil {
			return err
		}
		if bt == nil {
			return nil // other RVM / bad frame — keep waiting
		}
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

// DynamicsGNSS is a decoded dynamics.vehicle.gnss frame. Field numbers
// confirmed from a live frame (see parallax_test.go): lat/lon/alt are
// doubles, speed/heading floats, timestamp a varint (epoch ms). Fields
// 6-9 are GPS accuracy metrics we don't consume yet. speed (field 4)
// was absent (0) in the parked capture — mapped here, to be confirmed
// against a moving frame.
type DynamicsGNSS struct {
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	AltitudeM   float64 `json:"altitude_m"`
	SpeedMS     float64 `json:"speed_ms"`
	HeadingDeg  float64 `json:"heading_deg"`
	TimestampMs int64   `json:"timestamp_ms"`
}

// decodeDynamicsGNSS parses a dynamics.vehicle.gnss protobuf payload.
func decodeDynamicsGNSS(b64 string) (*DynamicsGNSS, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		if raw, err = base64.RawStdEncoding.DecodeString(b64); err != nil {
			return nil, fmt.Errorf("base64: %w", err)
		}
	}
	g := &DynamicsGNSS{}
	err = pbScan(raw, func(field, wire int, val []byte) {
		switch {
		case field == 1 && wire == 1:
			g.Latitude = math.Float64frombits(binary.LittleEndian.Uint64(val))
		case field == 2 && wire == 1:
			g.Longitude = math.Float64frombits(binary.LittleEndian.Uint64(val))
		case field == 3 && wire == 1:
			g.AltitudeM = math.Float64frombits(binary.LittleEndian.Uint64(val))
		case field == 4 && wire == 5:
			g.SpeedMS = float64(math.Float32frombits(binary.LittleEndian.Uint32(val)))
		case field == 5 && wire == 5:
			g.HeadingDeg = float64(math.Float32frombits(binary.LittleEndian.Uint32(val)))
		case field == 10 && wire == 0:
			v, _ := binary.Uvarint(val)
			g.TimestampMs = int64(v)
		}
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// ParallaxRawFrame is one captured Parallax message: the RVM topic, its
// server timestamp, and the raw base64 protobuf payload. Used to
// reverse-engineer a topic's wire shape from live frames before writing
// a decoder (the reliable path: field layouts are confirmed against real
// frames, not assumed).
type ParallaxRawFrame struct {
	RVM        string `json:"rvm"`
	Timestamp  string `json:"timestamp"`
	PayloadB64 string `json:"payload_b64"`
}

// ProbeParallaxRaw subscribes to the given RVM topics and returns up to
// maxFrames raw frames, or whatever arrived before the timeout. The
// vehicle must be awake (and, for dynamics.vehicle.gnss, moving) for
// frames to stream.
func (c *LiveClient) ProbeParallaxRaw(ctx context.Context, vehicleID string, rvms []string, maxFrames int) ([]ParallaxRawFrame, error) {
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return nil, ErrNotAuthenticated
	}
	if vehicleID == "" || len(rvms) == 0 {
		return nil, errors.New("rivian: vehicleID and rvms are required")
	}
	if maxFrames <= 0 {
		maxFrames = 5
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	var frames []ParallaxRawFrame
	err := c.runGenericSubscription(ctx, userTok, subParams{
		operationName: "ParallaxMessages",
		query:         qParallaxMessagesSubscription,
		vehicleID:     vehicleID,
		variables:     map[string]any{"vehicleId": vehicleID, "rvms": rvms},
	}, func(raw json.RawMessage) error {
		var p parallaxNext
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil
		}
		if len(p.Errors) > 0 {
			return fmt.Errorf("parallax subscription error: %s", p.Errors[0].Message)
		}
		msg := p.Data.ParallaxMessages
		if msg.Payload == "" {
			return nil
		}
		frames = append(frames, ParallaxRawFrame{
			RVM:        msg.RVM,
			Timestamp:  strings.Trim(string(msg.Timestamp), `"`),
			PayloadB64: msg.Payload,
		})
		if len(frames) >= maxFrames {
			return errParallaxDone
		}
		return nil
	})
	if len(frames) > 0 {
		return frames, nil
	}
	if err != nil && !errors.Is(err, errParallaxDone) {
		return nil, err
	}
	return nil, errors.New("parallax: no frames within timeout (is the vehicle awake / moving?)")
}

// BatteryTempCallback receives decoded pack temperatures per frame.
type BatteryTempCallback func(*BatteryTemp)

// SubscribeBatteryState streams the energy.high_voltage.battery_state
// Parallax topic for vehicleID, invoking cb with decoded pack cell
// temperatures on every frame. Blocks until ctx is cancelled or auth
// fails; reconnects with exponential backoff on transient errors. The
// topic only pushes while the vehicle is awake.
//
// This is the additive-Parallax pattern (see docs on the pack-temp
// migration): vehicleState stays the primary source, and a Parallax
// topic is layered on for a field it lacks. The next topic Rivolt wants
// (cold-weather SOC, richer thermal, …) follows this same shape.
func (c *LiveClient) SubscribeBatteryState(ctx context.Context, vehicleID string, cb BatteryTempCallback) error {
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return ErrNotAuthenticated
	}
	if vehicleID == "" {
		return errors.New("rivian: vehicleID is required")
	}
	if cb == nil {
		return errors.New("rivian: callback is required")
	}

	var bo reconnectBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		bo.start()
		err := c.runGenericSubscription(ctx, userTok, subParams{
			operationName: "ParallaxMessages",
			query:         qParallaxMessagesSubscription,
			vehicleID:     vehicleID,
			variables: map[string]any{
				"vehicleId": vehicleID,
				"rvms":      []string{rvmBatteryState},
			},
		}, func(raw json.RawMessage) error {
			bt, perr := parseBatteryFrame(raw)
			if perr != nil {
				// A subscription-level error is terminal (reconnect); a
				// single malformed payload returns nil bt + nil err.
				return perr
			}
			if bt != nil {
				cb(bt)
			}
			return nil
		})
		if err == nil {
			return ctx.Err()
		}
		if errors.Is(err, errWSUnauthenticated) {
			return err
		}
		wait := bo.next()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// DynamicsGNSSCallback receives a decoded gnss frame.
type DynamicsGNSSCallback func(*DynamicsGNSS)

// SubscribeDynamicsGNSS streams the dynamics.vehicle.gnss Parallax topic
// for vehicleID, invoking cb with each decoded frame. Blocks until ctx
// is cancelled or auth fails; reconnects with backoff. Measurement step
// of the driving-stats migration — run alongside vehicleState to compare
// GPS cadence/accuracy before flipping the recorder.
func (c *LiveClient) SubscribeDynamicsGNSS(ctx context.Context, vehicleID string, cb DynamicsGNSSCallback) error {
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return ErrNotAuthenticated
	}
	if vehicleID == "" {
		return errors.New("rivian: vehicleID is required")
	}
	if cb == nil {
		return errors.New("rivian: callback is required")
	}
	var bo reconnectBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		bo.start()
		err := c.runGenericSubscription(ctx, userTok, subParams{
			operationName: "ParallaxMessages",
			query:         qParallaxMessagesSubscription,
			vehicleID:     vehicleID,
			variables:     map[string]any{"vehicleId": vehicleID, "rvms": []string{"dynamics.vehicle.gnss"}},
		}, func(raw json.RawMessage) error {
			var p parallaxNext
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil
			}
			if len(p.Errors) > 0 {
				return fmt.Errorf("parallax subscription error: %s", p.Errors[0].Message)
			}
			msg := p.Data.ParallaxMessages
			if msg.RVM != "dynamics.vehicle.gnss" || msg.Payload == "" {
				return nil
			}
			if g, derr := decodeDynamicsGNSS(msg.Payload); derr == nil {
				cb(g)
			}
			return nil
		})
		if err == nil {
			return ctx.Err()
		}
		if errors.Is(err, errWSUnauthenticated) {
			return err
		}
		wait := bo.next()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// parseBatteryFrame decodes a Parallax "next" frame into a BatteryTemp.
// Returns (nil, nil) for frames of other RVM topics or a single
// malformed payload (tolerated to keep the stream alive), and a non-nil
// error only for a subscription-level GraphQL error (terminal).
func parseBatteryFrame(raw json.RawMessage) (*BatteryTemp, error) {
	var p parallaxNext
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil
	}
	if len(p.Errors) > 0 {
		return nil, fmt.Errorf("parallax subscription error: %s", p.Errors[0].Message)
	}
	msg := p.Data.ParallaxMessages
	if msg.RVM != rvmBatteryState || msg.Payload == "" {
		return nil, nil
	}
	bt, err := decodeBatteryTemp(msg.Payload)
	if err != nil {
		return nil, nil // tolerate a single bad frame
	}
	bt.Timestamp = strings.Trim(string(msg.Timestamp), `"`)
	bt.RawB64 = msg.Payload
	return bt, nil
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
	bt := &BatteryTemp{CellAvgC: float64(avg), CellMaxC: float64(max), CellMinC: float64(min)}
	// charge_state (battery_state field 1) carries SoC + pack capacity as
	// float64 (wire type 1), confirmed from a live frame: field 1 = SoC %
	// (67.4 vs vehicleState ~68.7), field 2 = pack capacity kWh (~123.4,
	// the "chargeKwh" degradation signal, more accurate than the nameplate
	// lookup). Best-effort — absent on frames without charge_state.
	if cs, ok, _ := pbMessageField(raw, 1); ok {
		if soc, ok2, _ := pbDoubleField(cs, 1); ok2 {
			bt.SoCPct = soc
		}
		if cap, ok2, _ := pbDoubleField(cs, 2); ok2 {
			bt.PackKWh = cap
		}
	}
	return bt, nil
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

// pbDoubleField returns the float64 value of a fixed64 (wire type 1)
// field `want`. ok=false when absent.
func pbDoubleField(buf []byte, want int) (float64, bool, error) {
	var out float64
	found := false
	err := pbScan(buf, func(field, wire int, val []byte) {
		if field == want && wire == 1 && len(val) == 8 {
			out = math.Float64frombits(binary.LittleEndian.Uint64(val))
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

// --- Drive-dynamics topics (Phase 2: vehicleState → Parallax) -------------
//
// Captured live from an R1S via /api/parallax-raw (2026-07-13). All three are
// single-field messages carrying one varint in field 1:
//
//	dynamics.vehicle.gear        08 01        -> 1      (P; see gearFromParallax)
//	dynamics.vehicle.drive_mode  08 02        -> 2      (driveMode enum, proto g70/*)
//	dynamics.vehicle.odometer    08 c3 d7 03  -> 60355  (whole kilometers:
//	                                            60355 km = 37502.9 mi vs
//	                                            vehicleState's 37503.06 mi)
//
// Recorded in shadow (measurement) alongside vehicleState before any field
// goes authoritative — see StateMonitor.driveDynamicsSubscriber and the
// migration doc's measure-first rule.
const (
	rvmDriveGear = "dynamics.vehicle.gear"
	rvmDriveMode = "dynamics.vehicle.drive_mode"
	rvmOdometer  = "dynamics.vehicle.odometer"
	// rvmPowerState is the vehicle-domain power/sleep-state topic (maps to
	// legacy powerState). Folded into the drive-dynamics shadow as an
	// earlier wake signal for the late-recording-start case. Wire shape not
	// yet confirmed — the shadow logs its raw payload for RE.
	rvmPowerState = "vehicle.power.state"
	// rvmRange (distanceToEmpty) and rvmTires (tirePressure{FL,FR,RL,RR})
	// are the next Path-B fields. Subscribed for raw-payload shadow capture
	// so their wire shapes can be RE'd from the logs before decoding.
	rvmRange = "dynamics.vehicle.range"
	rvmTires = "dynamics.tires.state"
	// rvmCabinTemps/rvmClosures/rvmLocks are decoded and authoritative,
	// so they live in the base drive-dynamics list (arrive in prod).
	rvmCabinTemps = "comfort.cabin.cabin_temperatures"
	rvmClosures   = "body.closures.states"
	rvmLocks      = "body.locks.states"
	// Remaining Path-B read-state topics,
	// carried only under the capture flag for raw-payload shadow RE until
	// each is decoded and promoted into the base list above. (Windows are
	// NOT a separate topic — the R1S reports them on body.closures.states
	// as instances 12-15; the standalone body.windows.states never emits.)
	rvmTrailer      = "body.trailer.state"
	rvmLowVoltBatt  = "energy.low_voltage.battery_state"
	rvmOtaState     = "ota.ota_state.vehicle_ota_state"
	rvmOtaDeploy    = "ota.deployment.state"
	rvmAlarmState   = "security.alarm.state"
	rvmPreconStatus = "comfort.cabin.cabin_preconditioning_status"
	// Read topics still pending their first live wire-shape capture (the
	// focused RVM message differs from the APK's combined-message field
	// numbers, so they must be RE'd from a real frame like cabin_temp was).
	rvmPetMode     = "comfort.cabin.pet_mode_status"
	rvmClimateHold = "comfort.cabin.climate_hold_status"
	rvmImmobilizer = "security.access.immobilizer_state"
	// rvmChargingStatus is charging.session.status, the
	// plug indicator an R2's vehicleState lacks: {1: plug_connection_status,
	// 2: display_status, 3: evse_type}. The app writes it into its own
	// vehicleState chargerStatus/chargerState (see chargerStatusFromParallax).
	rvmChargingStatus = "charging.session.status"
	// rvmTimeEstimation is charging.session.time_estimation: {1: validity
	// flag (an R2 never sends it), 2: remaining_minutes}. It counts down
	// smoothly while charging (a captured R2 session: 288 to 2 min).
	rvmTimeEstimation = "charging.session.time_estimation"
	// rvmBatteryChars is energy.high_voltage.battery_characteristics, the
	// pack's identity and usable capacity. Not subscribed: the Oct 2026
	// capture showed its user_total_kwh equals vehicleState's capacity, and
	// no fleet pack is LFP (scratchpad r2-gaps). decodeBatteryCharacteristics
	// stays, pinned and tested, for whoever shows the pack chemistry.
	rvmBatteryChars = "energy.high_voltage.battery_characteristics"
)

// CaptureRVMs are the read topics carried on the drive-dynamics
// subscription only under the preview capture flag, so prod isn't
// subscribed to them yet. alarm + 12V health are decoded from the APK
// enums and applied on preview (with the concurrent vehicleState value
// logged for verification); trailer + precon are decode-logged only
// (no State field / unconfirmed field number); ota_state is still on the
// raw default case. Graduate each into the base list above once the logs
// confirm the mapping (ota.deployment.state has: decodeOTADeployment).
var CaptureRVMs = []string{
	rvmTrailer,
	rvmLowVoltBatt, rvmOtaState, rvmAlarmState, rvmPreconStatus,
	rvmPetMode, rvmClimateHold, rvmImmobilizer,
}

// body.closures.states / body.locks.states enum values.
// Both are repeated {1: instance, 2: status}. Closure instances: 1-4 doors
// (row1 L/R, row2 L/R), 5 frunk, 6 tailgate, 7 liftgate, 11 tonneau, 12-15
// windows. Lock instances: 1-4 doors. Status is meaningful only when
// non-zero (0 = unspecified, e.g. a closure the vehicle doesn't have).
//
// Window instance→corner mapping confirmed live on the R1S (2026-07-17):
// opening the driver window flipped instance 12 to OPEN, the passenger
// window flipped 13 — mirroring the door ordering, so 14/15 are the rears.
// The standalone body.windows.states topic never emits; windows are
// reported here on the closures topic.
const (
	closureStatusClose = 2 // 1=OPEN, 2=CLOSE, 3=AJAR, 4=OPENING, 5=CLOSING
	lockStatusLocked   = 1 // 1=LOCKED, 2=UNLOCKED, 3=PARTIALLY_UNLOCKED
	closInstFrunk      = 5
	closInstTailgate   = 6
	closInstLiftgate   = 7
	closInstTonneau    = 11
	closInstWindowFL   = 12
	closInstWindowFR   = 13
	closInstWindowRL   = 14
	closInstWindowRR   = 15
)

// decodeInstanceStates parses a repeated {1: instance, 2: status} message
// (closures or locks) into an instance→status map. status 0 (unspecified)
// is kept so callers can distinguish "absent" from a real state.
func decodeInstanceStates(b64 string) (map[int]int, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	out := map[int]int{}
	_ = pbScan(raw, func(f, w int, val []byte) {
		if f != 1 || w != 2 {
			return
		}
		inst, status, haveInst := 0, 0, false
		_ = pbScan(val, func(sf, sw int, sv []byte) {
			if sw != 0 {
				return
			}
			n, _ := binary.Uvarint(sv)
			switch sf {
			case 1:
				inst, haveInst = int(n), true
			case 2:
				status = int(n)
			}
		})
		if haveInst {
			out[inst] = status
		}
	})
	return out, len(out) > 0
}

// decodeCabinTemp pulls the interior cabin temperature (field 3, float32,
// °C) from a comfort.cabin.cabin_temperatures payload. Confirmed live:
// 26.0 == vehicleState inside_temp_c; field 4 is the driver setpoint.
func decodeCabinTemp(b64 string) (float64, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0, false
	}
	if v, ok, _ := pbFloatField(raw, 3); ok {
		return float64(v), true
	}
	return 0, false
}

// decodeSingleVarint decodes a base64 protobuf payload of the shape
// { field 1: varint } and returns that varint. Extra fields are ignored so a
// firmware that later grows these messages doesn't break the read; ok=false
// means the payload was malformed or carried no field-1 varint.
func decodeSingleVarint(b64 string) (val uint64, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0, false
	}
	if err := pbScan(raw, func(field, wire int, v []byte) {
		if field == 1 && wire == 0 {
			if n, m := binary.Uvarint(v); m > 0 {
				val, ok = n, true
			}
		}
	}); err != nil {
		return 0, false
	}
	return val, ok
}

// gearFromParallax maps the dynamics.vehicle.gear enum to the P/R/N/D
// contract normalizeGear produces from legacy vehicleState. P/R/D (1/2/4)
// are confirmed from live captures — on the 2026-07-14 drive the Parallax
// frame led the matching vehicleState transition by ~2s. N (3) is inferred
// from the P/R/N/D = 1/2/3/4 ordering (not shifted through yet). Returns ""
// for an unmapped value so callers fall back to vehicleState.
func gearFromParallax(v uint64) string {
	switch v {
	case 1:
		return "P"
	case 2:
		return "R"
	case 3:
		return "N" // inferred (ordering), not yet observed live
	case 4:
		return "D"
	default:
		return ""
	}
}

// powerStateFromParallax maps the vehicle.power.state enum to the
// powerState string contract vehicleState uses. 3=ready and 4=go are
// confirmed from the 2026-07-14 drive (3→4 led drive-start, 4→3 led
// drive-end). 1=sleep from the fleet (devils-advocate's R2 audit, Oct
// 15): on R1s enum 1 coincides with vehicleState "sleep" (2,003 / 47
// and 1,530 / 0 frames on two trucks). 2=standby is weaker evidence
// (mixed vehicleState values on R1s) but sits between ready and sleep
// as the car settles. 5=vehicle_reset, 6=ota_update and 7=shutdown follow
// the proto declaration order (UNDEFINED, SLEEP, STANDBY, READY, GO,
// VEHICLE_RESET, OTA_UPDATE, SHUTDOWN) and use the vehicleState vocabulary;
// 6 is confirmed live (22 frames co-occurring with vehicleState ota_update
// over 7 days), 5 is consistent (2 frames, with ready), 7 is unseen. An R2
// has no vehicleState powerState, so without these a mid-OTA R2 kept its
// last state, often "sleep". Returns "" for an unmapped value so callers
// fall back to vehicleState.
func powerStateFromParallax(v uint64) string {
	switch v {
	case 1:
		return "sleep"
	case 2:
		return "standby"
	case 3:
		return "ready"
	case 4:
		return "go"
	case 5:
		return "vehicle_reset"
	case 6:
		return "ota_update"
	case 7:
		return "shutdown"
	default:
		return ""
	}
}

// driveModeFromParallax maps the dynamics.vehicle.drive_mode enum to the
// lowercase driveMode string vehicleState reports (client.go: "everyday" |
// "sport" | …). Numbers follow the drive-mode enum, anchored by the live
// capture (2 = EVERYDAY, the default). UNSPECIFIED/INIT_MODE/FAULT (0/1/7) return "" so those
// non-display states fall back to vehicleState.
func driveModeFromParallax(v uint64) string {
	switch v {
	case 2:
		return "everyday"
	case 3:
		return "off_road_snow_ice"
	case 4:
		return "off_road_sport_auto"
	case 5:
		return "off_road_sport_drift"
	case 6:
		return "sport_launch"
	case 8:
		return "sport"
	case 9:
		return "distance"
	case 10:
		return "towing"
	case 11:
		return "off_road_auto"
	case 12:
		return "off_road_sand"
	case 13:
		return "off_road_rocks"
	case 14:
		return "off_road_mud"
	case 15:
		return "winter"
	default:
		return "" // 0 UNSPECIFIED, 1 INIT_MODE, 7 FAULT → vehicleState
	}
}

// The following four map single-varint (field 1) Parallax status enums to
// the lowercase strings the UI formatters already understand (see
// LivePanel formatBoolish / formatTwelveVolt / formatPrecondition). The
// numbers follow the topics' protobuf enums. 0 (UNSPECIFIED) and any unknown value return "" so the
// field falls back to vehicleState.

// alarmSoundFromParallax: security.alarm.state field 1 (sound_alarm).
// SOUND_ALARM_STATUS: 0 UNSPECIFIED, 1 FALSE, 2 TRUE, 3 SNA.
func alarmSoundFromParallax(v uint64) string {
	switch v {
	case 1:
		return "false"
	case 2:
		return "true"
	default:
		return "" // UNSPECIFIED / SNA → vehicleState
	}
}

// lowVoltHealthFromParallax: energy.low_voltage.battery_state field 1.
// LOW_VOLTAGE_BATTERY_HEALTH_STATUS: 0 UNSPECIFIED, 1 NORMAL, 2 LOW.
func lowVoltHealthFromParallax(v uint64) string {
	switch v {
	case 1:
		return "normal"
	case 2:
		return "low"
	default:
		return ""
	}
}

// trailerPresenceFromParallax: body.trailer.state field 1.
// TRAILER_PRESENCE_STATUS: 0 UNSPECIFIED, 1 NOT_PRESENT, 2 PRESENT,
// 3 PRESENT_WITH_BRAKES, 4 INVALID. No State destination field yet, so
// this is decode-for-logging only.
func trailerPresenceFromParallax(v uint64) string {
	switch v {
	case 1:
		return "not_present"
	case 2:
		return "present"
	case 3:
		return "present_with_brakes"
	case 4:
		return "invalid"
	default:
		return ""
	}
}

// cabinPreconStateFromParallax: comfort.cabin.cabin_preconditioning_status.
// CABIN_PRECONDITIONING_STATE enum (0-9). Mapped to the small vocabulary
// formatPrecondition expects. NOTE: the topic's field number is not yet
// confirmed by a live capture (the car wasn't preconditioning during RE),
// so this is decode-for-logging only until verified.
func cabinPreconStateFromParallax(v uint64) string {
	switch v {
	case 1, 2, 3: // INITIATE, ACTIVE, ACTIVE_WARNING
		return "preconditioning"
	case 4: // COMPLETE_MAINTAIN
		return "preconditioned"
	case 8: // UNAVAILABLE
		return "not_preconditioning"
	default:
		return "" // UNSPECIFIED / timeouts / errors → vehicleState
	}
}

// petModeFromParallax: comfort.cabin.pet_mode_status field 1 (confirmed
// live: 08 02 = DISABLED). PET_MODE_STATE (APK): 0 OFF, 1 ON, 2 DISABLED,
// 3 FAULTY. Mapped to lowercase strings; isTruthyFlag treats "on" as the
// only active state, so off/disabled/faulty hide the pet-mode chip.
func petModeFromParallax(v uint64) string {
	switch v {
	case 0:
		return "off"
	case 1:
		return "on"
	case 2:
		return "disabled"
	case 3:
		return "faulty"
	default:
		return ""
	}
}

// TirePressure is one decoded wheel from a dynamics.tires.state frame.
// Position 1=FL, 2=FR, 3=RL, 4=RR; Bar is the pressure in bar (matches
// vehicleState's tire_pressure_*_bar). Confirmed live 2026-07-14: pressures
// 3.25/3.28/3.25/3.25 bar vs vehicleState tire_min_bar 3.25.
type TirePressure struct {
	Position int
	Bar      float64
}

// decodeTires parses a dynamics.tires.state payload:
//
//	message TiresState { ...; repeated TireState tires = 2; }
//	message TireState  { uint32 position = 1; ... = 2; double pressure_bar = 3;
//	                     ...; uint64 timestamp_ms = 5; }
func decodeTires(b64 string) ([]TirePressure, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, false
	}
	var out []TirePressure
	if err := pbScan(raw, func(field, wire int, val []byte) {
		if field != 2 || wire != 2 {
			return
		}
		var tp TirePressure
		_ = pbScan(val, func(f, w int, v []byte) {
			switch {
			case f == 1 && w == 0:
				if n, m := binary.Uvarint(v); m > 0 {
					tp.Position = int(n)
				}
			case f == 3 && w == 1 && len(v) == 8:
				tp.Bar = math.Float64frombits(binary.LittleEndian.Uint64(v))
			}
		})
		if tp.Position >= 1 && tp.Position <= 4 {
			out = append(out, tp)
		}
	}); err != nil {
		return nil, false
	}
	return out, len(out) > 0
}

// DriveDynamicsFrame is one shadow frame. Value is the field-1 varint (when
// ValueOK) — interpret per RVM (gear enum, drive_mode enum, odometer in whole
// km). Payload is the raw base64 so a not-yet-RE'd topic (e.g. power.state)
// can still be logged and decoded offline.
type DriveDynamicsFrame struct {
	RVM         string
	Value       uint64
	ValueOK     bool
	Payload     string
	TimestampMs int64
}

// DriveDynamicsCallback receives each decoded drive-dynamics frame.
type DriveDynamicsCallback func(DriveDynamicsFrame)

// SubscribeDriveDynamics streams the Parallax dynamics.vehicle.{gear,
// drive_mode,odometer} topics over one multiplexed subscription, invoking cb
// per decoded frame. Blocks until ctx is cancelled or auth fails; reconnects
// with backoff. Phase 2 measurement — run in shadow alongside vehicleState to
// pin the gear enum and compare cadence before any field is made
// authoritative.
//
// extra, if non-nil, is called before every (re)connect for the extra
// topics to carry, so a change (wake-safety capture switch) takes
// effect at the next reconnect without a forced one.
func (c *LiveClient) SubscribeDriveDynamics(ctx context.Context, vehicleID string, extra func() []string, cb DriveDynamicsCallback) error {
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return ErrNotAuthenticated
	}
	if vehicleID == "" {
		return errors.New("rivian: vehicleID is required")
	}
	if cb == nil {
		return errors.New("rivian: callback is required")
	}
	var bo reconnectBackoff
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rvms := []string{
			rvmDriveGear, rvmDriveMode, rvmOdometer, rvmPowerState,
			rvmRange, rvmTires, rvmCabinTemps, rvmClosures, rvmLocks,
			rvmChargingStatus, rvmOtaDeploy, rvmTimeEstimation,
		}
		if extra != nil {
			rvms = append(rvms, extra()...)
		}
		bo.start()
		err := c.runGenericSubscription(ctx, userTok, subParams{
			operationName: "ParallaxMessages",
			query:         qParallaxMessagesSubscription,
			vehicleID:     vehicleID,
			variables:     map[string]any{"vehicleId": vehicleID, "rvms": rvms},
		}, func(raw json.RawMessage) error {
			var p parallaxNext
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil
			}
			if len(p.Errors) > 0 {
				return fmt.Errorf("parallax subscription error: %s", p.Errors[0].Message)
			}
			msg := p.Data.ParallaxMessages
			if msg.Payload == "" {
				return nil
			}
			// Don't gate on a successful varint decode — a topic whose
			// shape isn't RE'd yet (power.state) must still reach the
			// shadow so its raw payload gets logged for offline decoding.
			v, ok := decodeSingleVarint(msg.Payload)
			var ts int64
			if s := strings.Trim(string(msg.Timestamp), `"`); s != "" {
				ts, _ = strconv.ParseInt(s, 10, 64)
			}
			cb(DriveDynamicsFrame{RVM: msg.RVM, Value: v, ValueOK: ok, Payload: msg.Payload, TimestampMs: ts})
			return nil
		})
		if err == nil {
			return ctx.Err()
		}
		if errors.Is(err, errWSUnauthenticated) {
			return err
		}
		wait := bo.next()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// ChargingStatus is one charging.session.status frame, validated against
// vehicleState's chargerStatus on live frames. Absent fields are 0, the
// proto default: plug INIT, display SNA.
type ChargingStatus struct {
	Plug    uint64 // 1: 0 INIT, 1 NOT_CONNECTED, 2 CONNECTED, 3 ERROR, 4 V2L_CONNECTED
	Display uint64 // 2: 0 SNA, 1 READY … 25 SMART_CHARGING_CHARGING (chargerStateFromParallax)
	EVSE    uint64 // 3: evse_type, enum not pinned
}

// decodeChargingStatus parses a charging.session.status payload. ok=false
// means the payload was malformed; an empty message is valid (all INIT).
func decodeChargingStatus(b64 string) (ChargingStatus, bool) {
	var cs ChargingStatus
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return cs, false
	}
	if err := pbScan(raw, func(f, w int, val []byte) {
		if w != 0 {
			return
		}
		v, n := binary.Uvarint(val)
		if n <= 0 {
			return
		}
		switch f {
		case 1:
			cs.Plug = v
		case 2:
			cs.Display = v
		case 3:
			cs.EVSE = v
		}
	}); err != nil {
		return cs, false
	}
	return cs, true
}

// chargerStatusFromParallax maps plug_connection_status to the vehicleState
// chargerStatus vocabulary the way the app does: CONNECTED is
// connected_no_chrg (the app never derives connected_charging from it; the
// session state says whether it charges), NOT_CONNECTED and V2L are
// not_connected, ERROR is fault. INIT and unknown values return "", which
// leaves the cached status alone.
func chargerStatusFromParallax(plug uint64) string {
	switch plug {
	case 1, 4:
		return "chrgr_sts_not_connected"
	case 2:
		return "chrgr_sts_connected_no_chrg"
	case 3:
		return "chrgr_sts_fault"
	default:
		return ""
	}
}

// chargerStateFromParallax maps display_status to the vehicleState
// chargerState vocabulary, as the app does. Logged next to vehicleState's
// value only; vehicleState still owns ChargerState. SNA (0), the smart
// charging states (24, 25) and unknown values return "".
func chargerStateFromParallax(display uint64) string {
	switch display {
	case 1:
		return "charging_ready"
	case 2:
		return "charging_connecting"
	case 3:
		return "charging_active"
	case 4:
		return "charging_complete"
	case 5:
		return "charging_scheduled"
	case 6:
		return "charging_vehicle_error"
	case 7:
		return "charging_station_error"
	case 8, 19:
		return "charging_user_stopped"
	case 9:
		return "charging_station_stopped"
	case 10, 20:
		return "charging_payment_error"
	case 11, 12, 21, 22, 23:
		return "charging_tls_error"
	case 13:
		return "charging_error_ac_adapter_used_on_dc"
	case 14:
		return "charging_error_dc_adapter_used_on_ac"
	case 15:
		return "charging_error_incompatible_charger"
	case 16:
		return "charging_sd_compensation"
	case 17:
		return "waiting_on_charger"
	case 18:
		return "charger_not_ready_or_incompatible"
	default:
		return ""
	}
}

// BatteryCharacteristics is one energy.high_voltage.battery_characteristics
// frame. The enums are logged raw; packCapacityKWh names
// the capacity one.
type BatteryCharacteristics struct {
	Chemistry    uint64  // 1: 1 NCA, 2 LFP, 3 NMC
	CellType     uint64  // 2: 1 50G, 2 53G, 3 G124, 4 LG_4695
	ModuleType   uint64  // 3: 1 9M, 2 1M, 3 8M, 4 6M, 5 11M, 6 7M, 7 3M, 8 10M
	PackCapacity uint64  // 4: see packCapacityKWh
	UserTotalKWh float32 // 5
	UserMaxKWh   float32 // 6
}

// decodeBatteryCharacteristics parses a battery_characteristics payload.
func decodeBatteryCharacteristics(b64 string) (BatteryCharacteristics, bool) {
	var bc BatteryCharacteristics
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return bc, false
	}
	if err := pbScan(raw, func(f, w int, val []byte) {
		switch {
		case w == 0 && f >= 1 && f <= 4:
			v, n := binary.Uvarint(val)
			if n <= 0 {
				return
			}
			switch f {
			case 1:
				bc.Chemistry = v
			case 2:
				bc.CellType = v
			case 3:
				bc.ModuleType = v
			case 4:
				bc.PackCapacity = v
			}
		case w == 5 && f == 5:
			bc.UserTotalKWh = math.Float32frombits(binary.LittleEndian.Uint32(val))
		case w == 5 && f == 6:
			bc.UserMaxKWh = math.Float32frombits(binary.LittleEndian.Uint32(val))
		}
	}); err != nil {
		return bc, false
	}
	return bc, true
}

// packCapacityKWh names the battery_characteristics pack_capacity enum in
// kWh; 0 for unspecified or unknown.
func packCapacityKWh(v uint64) float64 {
	switch v {
	case 1:
		return 135
	case 2:
		return 150
	case 3:
		return 100
	case 4:
		return 116
	case 5:
		return 108
	case 6:
		return 88
	default:
		return 0
	}
}

// decodeOTADeployment parses ota.deployment.state (validated against
// getOTAUpdateDetails on live frames):
//
//	1: repeated Software { 1: category (1 = FIRMWARE), 2: Version,
//	                       3: deployment_id, 4: AvailableOTA }
//	Version      { 1: version "2026.36.41", 2: software_version_id,
//	               3: year, 4: week, 5: number, 6: git_hash }
//	AvailableOTA { 1: deployment_id, 2: Version, 3: ota_type, 4: context,
//	               5: progress, 6: install_time_secs, 7: install_tod,
//	               8: skip_count, 9: skip_allowed, 10: intent, 11: is_active }
//
// current is the FIRMWARE software's version; available is the version of
// the last software whose AvailableOTA is active (""= none pending).
// ok=false means the payload was malformed or carried no firmware entry.
func decodeOTADeployment(b64 string) (current, available string, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", false
	}
	version := func(msg []byte) (v string) {
		_ = pbScan(msg, func(f, w int, val []byte) {
			if f == 1 && w == 2 {
				v = string(val)
			}
		})
		return v
	}
	haveFirmware := false
	err = pbScan(raw, func(f, w int, sw []byte) {
		if f != 1 || w != 2 {
			return
		}
		var category uint64
		var ver, availVer string
		active := false
		_ = pbScan(sw, func(sf, sw2 int, val []byte) {
			switch {
			case sf == 1 && sw2 == 0:
				category, _ = binary.Uvarint(val)
			case sf == 2 && sw2 == 2:
				ver = version(val)
			case sf == 4 && sw2 == 2:
				_ = pbScan(val, func(af, aw int, av []byte) {
					switch {
					case af == 2 && aw == 2:
						availVer = version(av)
					case af == 11 && aw == 0:
						n, _ := binary.Uvarint(av)
						active = n != 0
					}
				})
			}
		})
		if category == 1 && !haveFirmware {
			current, haveFirmware = ver, true
		}
		if active && availVer != "" {
			available = availVer
		}
	})
	if err != nil || !haveFirmware {
		return "", "", false
	}
	return current, available, true
}

// decodeTimeEstimation reads the remaining minutes (field 2) of a
// charging.session.time_estimation payload. ok=false: malformed, or no
// remaining-minutes field. The value may be a varint or a float.
func decodeTimeEstimation(b64 string) (mins float64, ok bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return 0, false
	}
	if err := pbScan(raw, func(f, w int, val []byte) {
		if f != 2 {
			return
		}
		switch w {
		case 0:
			if v, n := binary.Uvarint(val); n > 0 {
				mins, ok = float64(v), true
			}
		case 5:
			if len(val) >= 4 {
				mins, ok = float64(math.Float32frombits(binary.LittleEndian.Uint32(val))), true
			}
		}
	}); err != nil {
		return 0, false
	}
	if !ok || math.IsNaN(mins) || mins < 0 || mins > maxEstimateMins {
		return 0, false
	}
	return mins, true
}

// maxEstimateMins bounds a plausible time to full: three days.
const maxEstimateMins = 3 * 24 * 60
