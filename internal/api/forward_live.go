package api

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/apohor/rivolt/internal/forward"
	"github.com/apohor/rivolt/internal/rivian"
	"github.com/apohor/rivolt/internal/settings"
)

// liveSessionWire is the owner's answer to a forwarded live_session: what its monitor holds for the open charge, and nothing the requester
// cannot decorate with its own settings (the home rate stays on the requester's side).
type liveSessionWire struct {
	Session *rivian.LiveSession `json:"session"`
	Now     chargeNowWire       `json:"now"`
}

type chargeNowWire struct {
	EnergyKWh float64 `json:"energy_kwh"`
	PeakKW    float64 `json:"peak_kw"`
	Place     string  `json:"place"`
}

func wireOf(sess *rivian.LiveSession, now rivian.ChargeNow) liveSessionWire {
	return liveSessionWire{Session: sess, Now: chargeNowWire{EnergyKWh: now.EnergyKWh, PeakKW: now.PeakKW, Place: now.Place}}
}

func (w liveSessionWire) response(vehicleID string, cfg settings.ChargingConfig) liveSessionResponse {
	return decorateLiveSessionFor(vehicleID, w.Session, rivian.ChargeNow{EnergyKWh: w.Now.EnergyKWh, PeakKW: w.Now.PeakKW, Place: w.Now.Place}, cfg)
}

// ServeLiveSession is the owner's side of a forwarded live_session: the same read the live-session handler makes from the monitor.
func ServeLiveSession(mon *rivian.StateMonitor, vehicleID string) forward.Result {
	if mon == nil {
		return forward.Result{Status: http.StatusServiceUnavailable, Class: forward.ClassUnavailable, Body: json.RawMessage(`{}`)}
	}
	sess, now := mon.LiveSessionForCharge(vehicleID)
	raw, err := json.Marshal(wireOf(sess, now))
	if err != nil {
		return forward.Result{Status: http.StatusBadGateway, Class: forward.ClassInternal}
	}
	return forward.Result{Status: http.StatusOK, Body: raw}
}

// forwardLiveSession reads the open charge's session from the vehicle's group owner when this pod holds no tokens for the account. local is
// what this pod answers on its own when it serves the call itself (it holds the group, or the forward fell soft); a nil forwarder or an
// unknown group with no soft fallback is errOwnerUnavailable, which the handler writes as a retry-able 503.
func (sf *stateForward) forwardLiveSession(ctx context.Context, c rivian.Client, vehicleID string, local func() (liveSessionWire, error)) (liveSessionWire, error) {
	if sf == nil || sf.fw == nil || sf.groupOf == nil || !sf.forwardable(c) {
		return local()
	}
	group, ok := sf.groupOf(vehicleID)
	if !ok {
		if !sf.fw.NoGroup(forward.KindLiveSession) {
			return liveSessionWire{}, errOwnerUnavailable
		}
		return local()
	}
	var (
		w    liveSessionWire
		lerr error
		ran  bool
	)
	res, _, err := sf.fw.Do(ctx, forward.Call{Kind: forward.KindLiveSession, User: sf.user.String(), Group: group, Args: forward.LiveSessionArgs{VehicleID: vehicleID}},
		func(context.Context) (forward.Result, error) {
			ran = true
			w, lerr = local()
			return forward.Result{Status: http.StatusOK}, nil
		})
	if ran {
		return w, lerr
	}
	if err != nil {
		return liveSessionWire{}, err
	}
	if res.Status != http.StatusOK {
		return liveSessionWire{}, errOwnerUnavailable
	}
	var out liveSessionWire
	if err := json.Unmarshal(res.Body, &out); err != nil {
		return liveSessionWire{}, errOwnerUnavailable
	}
	return out, nil
}
