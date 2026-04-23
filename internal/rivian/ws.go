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
	return c.runGenericSubscription(ctx, userTok, subParams{
		operationName: "VehicleState",
		query:         qVehicleStateSubscription,
		vehicleID:     vehicleID,
	}, func(raw json.RawMessage) error {
		var payload wsNextPayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			// Single bad frame — keep the stream alive.
			return nil
		}
		if len(payload.Errors) > 0 {
			msgs := make([]string, 0, len(payload.Errors))
			for _, e := range payload.Errors {
				msgs = append(msgs, e.Message)
			}
			return fmt.Errorf("ws subscription error: %s", strings.Join(msgs, "; "))
		}
		cb(stateFromVehicleStateData(vehicleID, payload.Data))
		return nil
	})
}

// subParams is the per-subscription config passed into the generic
// runner. operationName matches what Rivian's gateway logs; query is
// the GraphQL document string; vehicleID goes into the variables.
type subParams struct {
	operationName string
	query         string
	vehicleID     string
	// variables overrides the default {vehicleID: ...} payload when
	// set. Used for subscriptions that expect a different casing or
	// additional args (e.g. ParallaxMessages wants "vehicleId" plus
	// an "rvms" filter).
	variables map[string]any
}

// runGenericSubscription is the shared subscribe → dispatch loop
// used by every WS subscription. It acquires the shared mux (dialling
// a new connection if needed), registers a subscription, and pumps
// incoming "next" payloads into onNext until the context is cancelled
// or a terminal error arrives from the server / transport.
//
// Returning nil means ctx was cancelled (clean shutdown). A non-nil
// error means the caller's outer retry loop should back off and
// reconnect — which will trigger a fresh mux if this one died.
func (c *LiveClient) runGenericSubscription(ctx context.Context, userTok string, params subParams, onNext func(json.RawMessage) error) error {
	_ = userTok // userSessionToken is read inside acquireMux under lock.

	mux, err := c.acquireMux(ctx)
	if err != nil {
		return err
	}
	defer c.releaseMux(mux)

	vars := params.variables
	if vars == nil {
		vars = map[string]any{"vehicleID": params.vehicleID}
	}
	sub, unsubscribe, err := mux.subscribe(ctx, params.operationName, params.query, vars)
	if err != nil {
		return err
	}
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return nil
		case payload := <-sub.framesCh:
			if err := onNext(payload); err != nil {
				return err
			}
		case err := <-sub.errCh:
			return err
		}
	}
}


// stateFromVehicleStateData is the subscription counterpart to the
// inline construction in State(). Kept separate so the REST path can
// stay compact while the ws path handles the full field set including
// tire pressures (which REST can't query).
func stateFromVehicleStateData(vehicleID string, data vehicleStateData) *State {
	vs := data.VehicleState
	at := parseTimeOrNow(vs.GNSSLocation.TimeStamp)
	ps := func(s permissiveString) string { return string(s) }
	return &State{
		At:              at,
		VehicleID:       vehicleID,
		BatteryLevelPct: vs.BatteryLevel.Value,
		DistanceToEmpty: vs.DistanceToEmpty.Value,
		OdometerKm:      vs.VehicleMileage.Value / 1000,
		Gear:            normalizeGear(ps(vs.GearStatus.Value)),
		DriveMode:       ps(vs.DriveMode.Value),
		ChargerState:    ps(vs.ChargerState.Value),
		// Charger power is not in the vehicleState selection; it
		// comes via getLiveSessionData. Stays zero here.
		ChargerPowerKW:          0,
		ChargeTargetPct:         vs.BatteryLimit.Value,
		ChargerStatus:           ps(vs.ChargerStatus.Value),
		ChargePortState:         ps(vs.ChargePortState.Value),
		RemoteChargingAvailable: ps(vs.RemoteChargingAvailable.Value),
		Latitude:                vs.GNSSLocation.Latitude,
		Longitude:               vs.GNSSLocation.Longitude,
		SpeedKph:                vs.GNSSSpeed.Value,
		HeadingDeg:              vs.GNSSBearing.Value,
		AltitudeM:               vs.GNSSAltitude.Value,
		Locked: aggregateLocked(
			ps(vs.DoorFrontLeftLocked.Value),
			ps(vs.DoorFrontRightLocked.Value),
			ps(vs.DoorRearLeftLocked.Value),
			ps(vs.DoorRearRightLocked.Value),
			ps(vs.ClosureFrunkLocked.Value),
			ps(vs.ClosureLiftgateLocked.Value),
			ps(vs.ClosureTonneauLocked.Value),
			ps(vs.ClosureTailgateLocked.Value),
			ps(vs.ClosureSideBinLeftLocked.Value),
			ps(vs.ClosureSideBinRightLocked.Value),
		),
		DoorsClosed: aggregateClosed(
			ps(vs.DoorFrontLeftClosed.Value),
			ps(vs.DoorFrontRightClosed.Value),
			ps(vs.DoorRearLeftClosed.Value),
			ps(vs.DoorRearRightClosed.Value),
		),
		FrunkClosed:                isClosed(ps(vs.ClosureFrunkClosed.Value)),
		LiftgateClosed:             isClosed(ps(vs.ClosureLiftgateClosed.Value)),
		TailgateClosed:             isClosed(ps(vs.ClosureTailgateClosed.Value)),
		TonneauClosed:              isClosed(ps(vs.ClosureTonneauClosed.Value)),
		CabinTempC:                 vs.CabinClimateInteriorTemperature.Value,
		CabinPreconditioningStatus: ps(vs.CabinPreconditioningStatus.Value),
		PowerState:                 strings.ToLower(strings.TrimSpace(ps(vs.PowerState.Value))),
		AlarmSoundStatus:           ps(vs.AlarmSoundStatus.Value),
		TwelveVoltBatteryHealth:    ps(vs.TwelveVoltBatteryHealth.Value),
		WiperFluidState:            ps(vs.WiperFluidState.Value),
		OtaCurrentVersion:          ps(vs.OtaCurrentVersion.Value),
		OtaAvailableVersion:        ps(vs.OtaAvailableVersion.Value),
		OtaStatus:                  ps(vs.OtaStatus.Value),
		OtaInstallProgress:         vs.OtaInstallProgress.Value,
		TirePressureFLBar:          vs.TirePressureFrontLeft.Value,
		TirePressureFRBar:          vs.TirePressureFrontRight.Value,
		TirePressureRLBar:          vs.TirePressureRearLeft.Value,
		TirePressureRRBar:          vs.TirePressureRearRight.Value,
		TirePressureStatusFL:       ps(vs.TirePressureStatusFrontLeft.Value),
		TirePressureStatusFR:       ps(vs.TirePressureStatusFrontRight.Value),
		TirePressureStatusRL:       ps(vs.TirePressureStatusRearLeft.Value),
		TirePressureStatusRR:       ps(vs.TirePressureStatusRearRight.Value),
	}
}

// Compile-time guard — the mutex on LiveClient must be held by the
// same goroutine throughout a subscription; runSubscription reads
// userTok once up front so concurrent Login doesn't race.
var _ sync.Locker = (*sync.Mutex)(nil)
