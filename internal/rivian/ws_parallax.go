package rivian

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"time"
)

// Parallax is Rivian's protobuf-over-GraphQL transport for data that
// the "graphs" tier of the mobile app surfaces — charging session
// breakdowns, parked energy, climate hold, OTA state, etc. The
// subscription pushes base64-encoded protobuf payloads tagged with
// their RVM (Remote Vehicle Module) topic. For charging, the
// interesting topic is energy_edge_compute.graphs.charge_session_breakdown,
// which populates for *every* session type including home AC / L1 /
// L2 / third-party chargers — unlike the ChargingSession subscription
// which only emits real data for Rivian Wall Charger / Adventure
// Network sessions.
//
// Source of truth: bretterer/rivian-python-client, src/rivian/proto/charging.py.

// rvmChargeSessionBreakdown is the RVM topic carrying ChargingSessionLiveData.
const rvmChargeSessionBreakdown = "energy_edge_compute.graphs.charge_session_breakdown"

// qParallaxMessagesSubscription streams Parallax RVM messages
// filtered to the charge-session breakdown. The server pushes a
// frame whenever the vehicle publishes a new snapshot on that topic
// (~every 10–30s while charging).
const qParallaxMessagesSubscription = `subscription ParallaxMessages($vehicleId: String!, $rvms: [String!]) {
  parallaxMessages(vehicleId: $vehicleId, rvms: $rvms) {
    payload
    timestamp
    rvm
  }
}`

// parallaxNext is the "next" frame envelope.
type parallaxNext struct {
	Data struct {
		ParallaxMessages struct {
			Payload   string `json:"payload"`
			Timestamp string `json:"timestamp"`
			RVM       string `json:"rvm"`
		} `json:"parallaxMessages"`
	} `json:"data"`
	Errors []graphQLError `json:"errors,omitempty"`
}

// chargingLiveData is the decoded ChargingSessionLiveData protobuf
// message. Field numbers match src/rivian/proto/charging.py.
type chargingLiveData struct {
	TotalKWh             float64 // 1
	PackKWh              float64 // 2
	ThermalKWh           float64 // 3
	OutletsKWh           float64 // 4
	SystemKWh            float64 // 5
	SessionDurationMins  int64   // 6
	TimeRemainingMins    int64   // 7
	RangeAddedKms        int64   // 8
	CurrentPowerKW       float64 // 9
	CurrentRangePerHour  int64   // 10
	// 11: SessionCost (nested message) — skipped; price comes from REST.
	IsFreeSession bool // 12
	ChargingState int64 // 13: 0=idle, 1=charging, 2=complete
}

// SubscribeParallaxCharging streams charge-session-breakdown RVM
// messages for vehicleID. Blocks until ctx is cancelled or auth
// fails; reconnects with exponential backoff on transient errors.
// This is the path that carries home-AC / L1 / L2 live power +
// energy data (the regular ChargingSession subscription returns
// nulls for those).
func (c *LiveClient) SubscribeParallaxCharging(ctx context.Context, vehicleID string, cb LiveSessionCallback) error {
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
		c.RecordChargingEvent(vehicleID, "parallax-open", "")
		err := c.runParallaxChargingSubscription(ctx, vehicleID, userTok, cb)
		if err != nil {
			c.RecordChargingEvent(vehicleID, "parallax-error", err.Error())
		} else {
			c.RecordChargingEvent(vehicleID, "parallax-close", "")
		}
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

func (c *LiveClient) runParallaxChargingSubscription(ctx context.Context, vehicleID, userTok string, cb LiveSessionCallback) error {
	framesLogged := 0
	return c.runGenericSubscription(ctx, userTok, subParams{
		operationName: "ParallaxMessages",
		query:         qParallaxMessagesSubscription,
		vehicleID:     vehicleID,
		variables: map[string]any{
			"vehicleId": vehicleID,
			"rvms":      []string{rvmChargeSessionBreakdown},
		},
	}, func(raw json.RawMessage) error {
		c.RecordChargingFrame(vehicleID, raw)
		var payload parallaxNext
		if err := json.Unmarshal(raw, &payload); err != nil {
			return nil
		}
		if len(payload.Errors) > 0 {
			msgs := ""
			for i, e := range payload.Errors {
				if i > 0 {
					msgs += "; "
				}
				msgs += e.Message
			}
			return fmt.Errorf("ws parallax subscription error: %s", msgs)
		}
		msg := payload.Data.ParallaxMessages
		if msg.RVM != rvmChargeSessionBreakdown || msg.Payload == "" {
			return nil
		}
		buf, err := base64.StdEncoding.DecodeString(msg.Payload)
		if err != nil {
			return nil // malformed single frame — keep stream alive
		}
		ld, err := decodeChargingLiveData(buf)
		if err != nil {
			return nil
		}
		if framesLogged < 3 {
			slog.Default().Info("rivian parallax charge-breakdown frame",
				"vehicle", vehicleID,
				"n", framesLogged,
				"power_kw", ld.CurrentPowerKW,
				"total_kwh", ld.TotalKWh,
				"range_km", ld.RangeAddedKms,
				"elapsed_min", ld.SessionDurationMins,
				"state", ld.ChargingState)
			framesLogged++
		}
		sess := &LiveSession{
			At:                    time.Now().UTC(),
			VehicleID:             vehicleID,
			Active:                ld.ChargingState == 1,
			VehicleChargerState:   chargingStateName(ld.ChargingState),
			TimeElapsedSeconds:    ld.SessionDurationMins * 60,
			TimeRemainingSeconds:  ld.TimeRemainingMins * 60,
			PowerKW:               ld.CurrentPowerKW,
			KilometersChargedPerHour: float64(ld.CurrentRangePerHour),
			RangeAddedKm:          float64(ld.RangeAddedKms),
			TotalChargedEnergyKWh: ld.TotalKWh,
			IsFreeSession:         ld.IsFreeSession,
		}
		cb(sess)
		return nil
	})
}

// chargingStateName maps the protobuf charging_state enum to the
// vehicleChargerState strings used elsewhere in the app.
func chargingStateName(s int64) string {
	switch s {
	case 1:
		return "charging_active"
	case 2:
		return "charging_complete"
	default:
		return ""
	}
}

// decodeChargingLiveData parses a ChargingSessionLiveData protobuf
// message. Only the fields we consume are decoded; nested
// SessionCost (field 11) and other complex/unknown fields are
// skipped safely via the wire-type length. Hand-rolled to avoid a
// protoc dependency for a single 13-field message.
func decodeChargingLiveData(buf []byte) (*chargingLiveData, error) {
	out := &chargingLiveData{}
	for len(buf) > 0 {
		tag, n := binary.Uvarint(buf)
		if n <= 0 {
			return nil, errors.New("parallax: bad tag varint")
		}
		buf = buf[n:]
		fieldNum := int(tag >> 3)
		wireType := int(tag & 0x7)
		switch wireType {
		case 0: // varint
			v, n := binary.Uvarint(buf)
			if n <= 0 {
				return nil, errors.New("parallax: bad varint")
			}
			buf = buf[n:]
			switch fieldNum {
			case 6:
				out.SessionDurationMins = int64(v)
			case 7:
				out.TimeRemainingMins = int64(v)
			case 8:
				out.RangeAddedKms = int64(v)
			case 10:
				out.CurrentRangePerHour = int64(v)
			case 12:
				out.IsFreeSession = v != 0
			case 13:
				out.ChargingState = int64(v)
			}
		case 1: // 64-bit (double)
			if len(buf) < 8 {
				return nil, errors.New("parallax: short double")
			}
			bits := binary.LittleEndian.Uint64(buf[:8])
			f := math.Float64frombits(bits)
			buf = buf[8:]
			switch fieldNum {
			case 1:
				out.TotalKWh = f
			case 2:
				out.PackKWh = f
			case 3:
				out.ThermalKWh = f
			case 4:
				out.OutletsKWh = f
			case 5:
				out.SystemKWh = f
			case 9:
				out.CurrentPowerKW = f
			}
		case 2: // length-delimited
			length, n := binary.Uvarint(buf)
			if n <= 0 {
				return nil, errors.New("parallax: bad length")
			}
			buf = buf[n:]
			if uint64(len(buf)) < length {
				return nil, errors.New("parallax: short bytes")
			}
			buf = buf[length:] // skip
		case 5: // 32-bit
			if len(buf) < 4 {
				return nil, errors.New("parallax: short fixed32")
			}
			buf = buf[4:]
		default:
			return nil, fmt.Errorf("parallax: unsupported wire type %d", wireType)
		}
	}
	return out, nil
}
