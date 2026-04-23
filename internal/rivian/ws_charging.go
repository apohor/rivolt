package rivian

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math/rand"
	"time"
)

// qChargingSessionSubscription matches the iOS app's ChargingSession
// subscription. Unlike the REST getLiveSessionHistory query — which
// returns active=false / all zeros for home AC / L1 / L2 sessions —
// this subscription pushes real telemetry for EVERY charging session
// including home AC. Discovered from rivian-python-client's
// subscribe_for_charging_session.
//
// Schema: chargingSession(vehicleId) { chartData {...} liveData {...} }
// Field names are flat scalars here (powerKW, not powerKW { value } —
// this subscription is NOT wrapped in valueRecord envelopes, unlike
// vehicleState).
const qChargingSessionSubscription = `subscription ChargingSession($vehicleID: String!) {
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

// chargingSessionNext is the envelope for a ChargingSession "next"
// frame. All scalar fields in liveData are populated on push even
// when getLiveSessionHistory returns active=false — Rivian's edge
// collector emits the subscription for every session type.
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

// LiveSessionCallback fires on every ChargingSession subscription
// push. The delivered LiveSession is marked Active (Rivian itself
// doesn't send an active flag on this subscription — the fact that
// we're receiving frames means the session is live).
type LiveSessionCallback func(*LiveSession)

// SubscribeChargingSession opens a websocket subscription to the
// ChargingSession stream for the given vehicle. Blocks until ctx is
// cancelled or a terminal auth failure occurs. Reconnects with
// exponential backoff on transient errors, identical to
// SubscribeVehicleState.
//
// Prefer this over the polling LiveSession REST call during active
// charging: the subscription pushes updates as the vehicle reports
// them (roughly every 30s) and returns populated data for home AC
// sessions the REST endpoint cannot see.
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
	firstFrameLogged := false
	return c.runGenericSubscription(ctx, userTok, subParams{
		operationName: "ChargingSession",
		query:         qChargingSessionSubscription,
		vehicleID:     vehicleID,
	}, func(raw json.RawMessage) error {
		// Log the first raw frame per connection so we can verify the
		// actual field shape Rivian pushes. Our query is a guess
		// derived from community reverse-engineering — if liveData
		// comes back empty / wrapped in valueRecord envelopes / etc
		// this is the only way to find out what the real schema is.
		if !firstFrameLogged {
			slog.Default().Info("rivian charging-session ws raw first frame",
				"vehicle", vehicleID,
				"raw", string(raw))
			firstFrameLogged = true
		}
		var payload chargingSessionNext
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil // single bad frame — keep stream alive
		}
		if len(payload.Errors) > 0 {
			// Surface errors so the outer reconnect loop backs off;
			// getting here typically means the schema changed again.
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
			// IsRivianCharger isn't in the subscription selection —
			// the REST response has it; we leave false here and let
			// the poller's cached value win when both are present.
		}
		cb(sess)
		return nil
	})
}
