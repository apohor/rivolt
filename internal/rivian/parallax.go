package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
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
// a decoder (the reliable path — the app's protobuf classes are
// R8-obfuscated and the nested layouts don't read cleanly from smali).
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

	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		wait := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond
		if wait > 5*time.Minute {
			wait = 5 * time.Minute
		}
		attempt++
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
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		wait := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond
		if wait > 5*time.Minute {
			wait = 5 * time.Minute
		}
		attempt++
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
)

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
// drive-end); the sleep enum isn't captured yet. Returns "" for an
// unmapped value so callers fall back to vehicleState.
func powerStateFromParallax(v uint64) string {
	switch v {
	case 3:
		return "ready"
	case 4:
		return "go"
	default:
		return ""
	}
}

// driveModeFromParallax maps the dynamics.vehicle.drive_mode enum to the
// lowercase driveMode string vehicleState reports (client.go: "everyday" |
// "sport" | …). Numbers are from the Rivian APK 3.14.0 proto enum
// (DRIVE_MODE_*_VALUE), anchored by the live capture (2 = EVERYDAY, the
// default). UNSPECIFIED/INIT_MODE/FAULT (0/1/7) return "" so those
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
func (c *LiveClient) SubscribeDriveDynamics(ctx context.Context, vehicleID string, cb DriveDynamicsCallback) error {
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
	rvms := []string{rvmDriveGear, rvmDriveMode, rvmOdometer, rvmPowerState, rvmRange, rvmTires}
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
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
		wait := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Intn(1000))*time.Millisecond
		if wait > 5*time.Minute {
			wait = 5 * time.Minute
		}
		attempt++
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}
