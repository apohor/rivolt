package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"time"
)

// qChargingSessionSubscription streams charging telemetry over the
// WebSocket endpoint. Unlike the REST getLiveSessionHistory — which
// returns zeros for home AC / L1 / L2 — this pushes populated data
// for every session type. Scalars are flat (not wrapped in
// valueRecord envelopes like vehicleState). The $vehicleID variable
// must be ID!; String! is accepted by Apollo but the resolver
// returns all-null payloads.
const qChargingSessionSubscription = `subscription ChargingSession($vehicleID: ID!) {
  chargingSession(vehicleId: $vehicleID) {
    __typename
    liveData {
      __typename
      powerKW
      kilometersChargedPerHour
      rangeAddedThisSession
      totalChargedEnergy
      timeElapsed
      timeRemaining
      price
      currency
      isFreeSession
      vehicleChargerState
      startTime
    }
    chartData {
      __typename
      soc
      powerKW
      startTime
      endTime
      timeEstimationValidityStatus
      vehicleChargerState
    }
  }
}`

// chargingSessionNext is the "next" frame envelope.
type chargingSessionNext struct {
	Data struct {
		ChargingSession struct {
			LiveData struct {
				PowerKW                  float64 `json:"powerKW"`
				KilometersChargedPerHour float64 `json:"kilometersChargedPerHour"`
				RangeAddedThisSession    float64 `json:"rangeAddedThisSession"`
				TotalChargedEnergy       float64 `json:"totalChargedEnergy"`
				TimeElapsed              int64   `json:"timeElapsed"`
				TimeRemaining            int64   `json:"timeRemaining"`
				Price                    string  `json:"price"`
				Currency                 string  `json:"currency"`
				IsFreeSession            bool    `json:"isFreeSession"`
				VehicleChargerState      string  `json:"vehicleChargerState"`
				StartTime                string  `json:"startTime"`
			} `json:"liveData"`
			ChartData []struct {
				SoC                 float64 `json:"soc"`
				PowerKW             float64 `json:"powerKW"`
				StartTime           string  `json:"startTime"`
				EndTime             string  `json:"endTime"`
				VehicleChargerState string  `json:"vehicleChargerState"`
			} `json:"chartData"`
		} `json:"chargingSession"`
	} `json:"data"`
	Errors []graphQLError `json:"errors,omitempty"`
}

// LiveSessionCallback fires on every ChargingSession push. Delivered
// sessions are marked Active — the subscription only emits while live.
type LiveSessionCallback func(*LiveSession)

// SubscribeChargingSession streams charging telemetry for vehicleID.
// Blocks until ctx is cancelled or auth fails; reconnects with
// exponential backoff on transient errors. Preferred over the REST
// LiveSession poll — this path carries home AC / L2 data.
func (c *LiveClient) SubscribeChargingSession(ctx context.Context, vehicleID string, cb LiveSessionCallback) error {
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
		err := c.runChargingSubscription(ctx, vehicleID, userTok, cb)
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

func (c *LiveClient) runChargingSubscription(ctx context.Context, vehicleID, userTok string, cb LiveSessionCallback) error {
	framesLogged := 0
	return c.runGenericSubscription(ctx, userTok, subParams{
		operationName: "ChargingSession",
		query:         qChargingSessionSubscription,
		vehicleID:     vehicleID,
	}, func(raw json.RawMessage) error {
		// Every frame gets dropped into the ring buffer for later
		// inspection via the debug HTTP endpoint. First few also go
		// to the structured log so they're visible without curling.
		c.RecordChargingFrame(vehicleID, raw)
		if framesLogged < 5 {
			slog.Default().Info("rivian charging-session ws raw frame",
				"vehicle", vehicleID,
				"n", framesLogged,
				"raw", string(raw))
			framesLogged++
		}
		var payload chargingSessionNext
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil // single bad frame — keep stream alive
		}
		if len(payload.Errors) > 0 {
			// Surface errors so the outer loop reconnects with backoff.
			msgs := ""
			for i, e := range payload.Errors {
				if i > 0 {
					msgs += "; "
				}
				msgs += e.Message
			}
			return errors.New("ws charging-session subscription error: " + msgs)
		}
		ld := payload.Data.ChargingSession.LiveData
		// Derive SoC from the most recent chartData bucket when available.
		soc := 0.0
		if n := len(payload.Data.ChargingSession.ChartData); n > 0 {
			soc = payload.Data.ChargingSession.ChartData[n-1].SoC
		}
		sess := &LiveSession{
			At:                       time.Now().UTC(),
			VehicleID:                vehicleID,
			Active:                   true, // subscription is push-only; a frame means it's live
			VehicleChargerState:      ld.VehicleChargerState,
			StartTime:                ld.StartTime,
			TimeElapsedSeconds:       ld.TimeElapsed,
			TimeRemainingSeconds:     ld.TimeRemaining,
			PowerKW:                  ld.PowerKW,
			KilometersChargedPerHour: ld.KilometersChargedPerHour,
			RangeAddedKm:             ld.RangeAddedThisSession,
			TotalChargedEnergyKWh:    ld.TotalChargedEnergy,
			SoCPct:                   soc,
			CurrentPrice:             ld.Price,
			CurrentCurrency:          ld.Currency,
			IsFreeSession:            ld.IsFreeSession,
			// IsRivianCharger comes from REST; left false here so the
			// poller's cached value wins on merge.
		}
		cb(sess)
		return nil
	})
}
