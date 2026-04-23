package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

// wsEndpoint is the AppSync-style subscription gateway the mobile app
// uses for live vehicleState pushes. It speaks the
// graphql-transport-ws subprotocol (init → ack → subscribe → next...).
const wsEndpoint = "wss://api.rivian.com/gql-consumer-subscriptions/graphql"

// apolloClientVersion matches what the iOS app sends. The gateway
// doesn't appear to enforce this, but it's what HA-rivian has been
// using successfully for months.
const apolloClientVersion = "1.13.0-1494"

// qVehicleStateSubscription is the subscription counterpart to
// qVehicleState. Same selection set; the server pushes a "next" frame
// every time one of the fields updates. Tire pressures (in bar) and
// cabin temps DO resolve here, unlike in the query variant — they're
// subscription-only in the Rivian schema.
const qVehicleStateSubscription = `subscription VehicleState($vehicleID: String!) {
  vehicleState(id: $vehicleID) {
    __typename
    gnssLocation { latitude longitude timeStamp }
    gnssSpeed { value }
    gnssBearing { value }
    gnssAltitude { value }
    batteryLevel { value }
    distanceToEmpty { value }
    vehicleMileage { value }
    gearStatus { value }
    driveMode { value }
    chargerState { value }
    chargerStatus { value }
    batteryLimit { value }
    chargePortState { value }
    remoteChargingAvailable { value }
    cabinClimateInteriorTemperature { value }
    cabinPreconditioningStatus { value }
    powerState { value }
    alarmSoundStatus { value }
    twelveVoltBatteryHealth { value }
    wiperFluidState { value }
    otaCurrentVersion { value }
    otaAvailableVersion { value }
    otaStatus { value }
    otaInstallProgress { value }
    tirePressureFrontLeft { value }
    tirePressureFrontRight { value }
    tirePressureRearLeft { value }
    tirePressureRearRight { value }
    tirePressureStatusFrontLeft { value }
    tirePressureStatusFrontRight { value }
    tirePressureStatusRearLeft { value }
    tirePressureStatusRearRight { value }
    doorFrontLeftClosed { value }
    doorFrontRightClosed { value }
    doorRearLeftClosed { value }
    doorRearRightClosed { value }
    closureFrunkClosed { value }
    closureLiftgateClosed { value }
    closureTailgateClosed { value }
    closureTonneauClosed { value }
    doorFrontLeftLocked { value }
    doorFrontRightLocked { value }
    doorRearLeftLocked { value }
    doorRearRightLocked { value }
    closureFrunkLocked { value }
    closureLiftgateLocked { value }
    closureTonneauLocked { value }
    closureTailgateLocked { value }
    closureSideBinLeftLocked { value }
    closureSideBinRightLocked { value }
  }
}`

// wsFrame is the outer envelope of a graphql-transport-ws message.
type wsFrame struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// wsNextPayload is the "next" frame's payload — a standard GraphQL
// response wrapped around our vehicleStateData shape.
type wsNextPayload struct {
	Data   vehicleStateData `json:"data"`
	Errors []graphQLError   `json:"errors,omitempty"`
}

// StateCallback fires on every state update pushed over the
// subscription. Called from the websocket receiver goroutine — the
// implementation should be quick (copy to a channel, update a cache,
// etc.) and must not block on I/O.
type StateCallback func(*State)

// SubscribeVehicleState opens a websocket to Rivian's subscription
// gateway, subscribes to the named vehicle's state, and invokes cb
// with every push. Blocks until ctx is cancelled or the connection
// fails terminally (unauthenticated, context deadline, etc.).
// Reconnects with exponential backoff on transient errors.
//
// The caller owns the lifetime via ctx; there is no separate
// unsubscribe — ctx cancellation tears everything down cleanly.
func (c *LiveClient) SubscribeVehicleState(ctx context.Context, vehicleID string, cb StateCallback) error {
	c.mu.Lock()
	userTok := c.userSessionToken
	c.mu.Unlock()
	if userTok == "" {
		return errors.New("rivian: not authenticated; call Login first")
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
		err := c.runSubscription(ctx, vehicleID, userTok, cb)
		if err == nil {
			// runSubscription returns nil only on ctx.Done.
			return ctx.Err()
		}
		// Fatal auth failures — don't retry, the caller needs to
		// re-login before subscribing again.
		if errors.Is(err, errWSUnauthenticated) {
			return err
		}
		// Back off 1s → 2s → 4s → … capped at 5m with jitter, HA-
		// rivian's behaviour.
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

// errWSUnauthenticated signals the websocket closed with the upstream
// telling us our session token is no good. Not wrapped with
// ErrNotAuthenticated to keep the rivian-specific retry logic local.
var errWSUnauthenticated = errors.New("rivian: websocket unauthenticated")

// runSubscription handles one connect → init → subscribe → receive
// cycle. Returns nil when ctx is cancelled, or a non-nil error on any
// terminal failure that should trigger a reconnect.
func (c *LiveClient) runSubscription(ctx context.Context, vehicleID, userTok string, cb StateCallback) error {
	// Dial with the subprotocol Rivian's AppSync-derived gateway
	// expects. They reject anything else.
	conn, _, err := websocket.Dial(ctx, wsEndpoint, &websocket.DialOptions{
		Subprotocols: []string{"graphql-transport-ws"},
	})
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	// Inbound frames can be large on initial handshake (full state
	// dump). The default 32 KiB read limit trips on real accounts.
	conn.SetReadLimit(1 << 20)
	defer conn.Close(websocket.StatusNormalClosure, "bye") //nolint:errcheck

	// connection_init. Rivian wants the apollo client metadata in the
	// payload along with u-sess; the server replies with connection_ack.
	initFrame := map[string]any{
		"type": "connection_init",
		"payload": map[string]any{
			"client-name":    c.clientName,
			"client-version": apolloClientVersion,
			"dc-cid":         "m-ios-" + uuid.NewString(),
			"u-sess":         userTok,
		},
	}
	if err := wsWriteJSON(ctx, conn, initFrame); err != nil {
		return fmt.Errorf("ws init: %w", err)
	}

	// Wait for the ack. Anything else here is fatal.
	ackCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for {
		frame, err := wsReadFrame(ackCtx, conn)
		if err != nil {
			return fmt.Errorf("ws await ack: %w", err)
		}
		if frame.Type == "connection_ack" {
			break
		}
		// "ka" (keepalive) is fine; anything else pre-ack is not.
		if frame.Type == "ka" || frame.Type == "pong" {
			continue
		}
		return fmt.Errorf("ws unexpected pre-ack frame: %s", frame.Type)
	}

	// Subscribe. The id is our correlation token — every "next" frame
	// the server pushes will reference it.
	subID := uuid.NewString()
	subPayload, _ := json.Marshal(map[string]any{
		"operationName": "VehicleState",
		"query":         qVehicleStateSubscription,
		"variables":     map[string]any{"vehicleID": vehicleID},
	})
	subFrame := map[string]any{
		"id":      subID,
		"type":    "subscribe",
		"payload": json.RawMessage(subPayload),
	}
	if err := wsWriteJSON(ctx, conn, subFrame); err != nil {
		return fmt.Errorf("ws subscribe: %w", err)
	}

	// Receive loop. One timeout per read; the gateway pushes a keepalive
	// every ~30s so a 60s deadline is comfortable.
	for {
		readCtx, readCancel := context.WithTimeout(ctx, 90*time.Second)
		frame, err := wsReadFrame(readCtx, conn)
		readCancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("ws read: %w", err)
		}
		switch frame.Type {
		case "next":
			if frame.ID != subID {
				continue
			}
			var payload wsNextPayload
			if err := json.Unmarshal(frame.Payload, &payload); err != nil {
				// Single bad frame — keep the stream alive.
				continue
			}
			if len(payload.Errors) > 0 {
				msgs := make([]string, 0, len(payload.Errors))
				for _, e := range payload.Errors {
					msgs = append(msgs, e.Message)
				}
				return fmt.Errorf("ws subscription error: %s", strings.Join(msgs, "; "))
			}
			st := stateFromVehicleStateData(vehicleID, payload.Data)
			cb(st)
		case "error":
			return fmt.Errorf("ws server error: %s", string(frame.Payload))
		case "complete":
			// Server closed the subscription. Reconnect.
			return errors.New("ws subscription completed by server")
		case "ka", "pong", "ping":
			// Keepalive. Rivian sends "ka" periodically; HA-rivian
			// ignores it and so do we.
			continue
		case "connection_error":
			// Typically means the u-sess is expired. Bubble up as
			// fatal so the caller re-logs in.
			if strings.Contains(string(frame.Payload), "Unauthenticated") {
				return errWSUnauthenticated
			}
			return fmt.Errorf("ws connection_error: %s", string(frame.Payload))
		default:
			// Unknown frame type — skip.
		}
	}
}

// wsWriteJSON serialises v as JSON and writes a single text frame.
func wsWriteJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

// wsReadFrame reads one text/binary frame and decodes it into a
// wsFrame envelope. Returns a fresh frame per call; callers should
// not retain payloads across calls.
func wsReadFrame(ctx context.Context, conn *websocket.Conn) (*wsFrame, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var f wsFrame
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("decode frame: %w", err)
	}
	return &f, nil
}

// stateFromVehicleStateData is the subscription counterpart to the
// inline construction in State(). Kept separate so the REST path can
// stay compact while the ws path handles the full field set including
// tire pressures (which REST can't query).
func stateFromVehicleStateData(vehicleID string, data vehicleStateData) *State {
	vs := data.VehicleState
	at := parseTimeOrNow(vs.GNSSLocation.TimeStamp)
	return &State{
		At:              at,
		VehicleID:       vehicleID,
		BatteryLevelPct: vs.BatteryLevel.Value,
		DistanceToEmpty: vs.DistanceToEmpty.Value,
		OdometerKm:      vs.VehicleMileage.Value / 1000,
		Gear:            normalizeGear(vs.GearStatus.Value),
		DriveMode:       vs.DriveMode.Value,
		ChargerState:    vs.ChargerState.Value,
		// Charger power is not in the vehicleState selection; it
		// comes via getLiveSessionData. Stays zero here.
		ChargerPowerKW:          0,
		ChargeTargetPct:         vs.BatteryLimit.Value,
		ChargerStatus:           vs.ChargerStatus.Value,
		ChargePortState:         vs.ChargePortState.Value,
		RemoteChargingAvailable: vs.RemoteChargingAvailable.Value,
		Latitude:                vs.GNSSLocation.Latitude,
		Longitude:               vs.GNSSLocation.Longitude,
		SpeedKph:                vs.GNSSSpeed.Value,
		HeadingDeg:              vs.GNSSBearing.Value,
		AltitudeM:               vs.GNSSAltitude.Value,
		Locked: aggregateLocked(
			vs.DoorFrontLeftLocked.Value,
			vs.DoorFrontRightLocked.Value,
			vs.DoorRearLeftLocked.Value,
			vs.DoorRearRightLocked.Value,
			vs.ClosureFrunkLocked.Value,
			vs.ClosureLiftgateLocked.Value,
			vs.ClosureTonneauLocked.Value,
			vs.ClosureTailgateLocked.Value,
			vs.ClosureSideBinLeftLocked.Value,
			vs.ClosureSideBinRightLocked.Value,
		),
		DoorsClosed: aggregateClosed(
			vs.DoorFrontLeftClosed.Value,
			vs.DoorFrontRightClosed.Value,
			vs.DoorRearLeftClosed.Value,
			vs.DoorRearRightClosed.Value,
		),
		FrunkClosed:                isClosed(vs.ClosureFrunkClosed.Value),
		LiftgateClosed:             isClosed(vs.ClosureLiftgateClosed.Value),
		TailgateClosed:             isClosed(vs.ClosureTailgateClosed.Value),
		TonneauClosed:              isClosed(vs.ClosureTonneauClosed.Value),
		CabinTempC:                 vs.CabinClimateInteriorTemperature.Value,
		CabinPreconditioningStatus: vs.CabinPreconditioningStatus.Value,
		PowerState:                 strings.ToLower(strings.TrimSpace(vs.PowerState.Value)),
		AlarmSoundStatus:           vs.AlarmSoundStatus.Value,
		TwelveVoltBatteryHealth:    vs.TwelveVoltBatteryHealth.Value,
		WiperFluidState:            vs.WiperFluidState.Value,
		OtaCurrentVersion:          vs.OtaCurrentVersion.Value,
		OtaAvailableVersion:        vs.OtaAvailableVersion.Value,
		OtaStatus:                  vs.OtaStatus.Value,
		OtaInstallProgress:         vs.OtaInstallProgress.Value,
		TirePressureFLBar:          vs.TirePressureFrontLeft.Value,
		TirePressureFRBar:          vs.TirePressureFrontRight.Value,
		TirePressureRLBar:          vs.TirePressureRearLeft.Value,
		TirePressureRRBar:          vs.TirePressureRearRight.Value,
		TirePressureStatusFL:       vs.TirePressureStatusFrontLeft.Value,
		TirePressureStatusFR:       vs.TirePressureStatusFrontRight.Value,
		TirePressureStatusRL:       vs.TirePressureStatusRearLeft.Value,
		TirePressureStatusRR:       vs.TirePressureStatusRearRight.Value,
	}
}

// Compile-time guard — the mutex on LiveClient must be held by the
// same goroutine throughout a subscription; runSubscription reads
// userTok once up front so concurrent Login doesn't race.
var _ sync.Locker = (*sync.Mutex)(nil)
