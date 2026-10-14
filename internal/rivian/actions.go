package rivian

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Per-wake attribution (battery-drain experiment, D3).
//
// Every request Rivolt makes to Rivian is noted here per vehicle:
// subscription opens (with their topics), our own unsubscribes, and
// every GraphQL HTTP call. When the sleep gate sees a car go from sleep
// to awake it logs "wake attribution" with the last of these before
// the wake and the ones around it (logWakeAttribution), so a couple of
// days of logs say whether a wake follows something we did.
//
// The wake is anchored on the car's own reported time when Rivian
// gives one (powerState.timeStamp, or the Parallax power.state frame's
// timestamp), not when we noticed. A reconnect delivers a replay
// frame, so the moment we notice a wake is often right after our own
// subscribe even when the car woke earlier.

// powerStateTimestamp (RIVOLT_POWER_STATE_TIMESTAMP=1) asks for
// powerState's timeStamp in the vehicleState query and subscription.
// Off by default until it's confirmed against the live schema on
// preview: Rivian's other vehicleState fields carry one, but an invalid
// selection would fail the whole vehicleState query. Without it, the
// anchor is the Parallax power.state frame time or when we saw it.
var powerStateTimestamp = ParseBoolEnv(os.Getenv("RIVOLT_POWER_STATE_TIMESTAMP"))

func withPowerStateTS(q string) string {
	if !powerStateTimestamp {
		return q
	}
	return strings.Replace(q, "powerState { value }", "powerState { timeStamp value }", 1)
}

// actionRing is how many actions are kept per vehicle: about 20
// minutes of one vehicle's reconnect churn (4 subscriptions every
// ~3 min), enough to see what preceded any wake.
const actionRing = 128

// RivoltAction is one request Rivolt made to Rivian.
type RivoltAction struct {
	At     time.Time
	Kind   string // "subscribe" | "unsubscribe" | "request"
	Op     string // GraphQL operation name
	Detail string // subscription topics, if any
}

func (a RivoltAction) String() string {
	s := a.Kind + " " + a.Op
	if a.Detail != "" {
		s += "(" + a.Detail + ")"
	}
	return s
}

// actionLog is a bounded per-vehicle ring. Account-level requests (no
// vehicle, e.g. getUserInfo) are kept under "".
type actionLog struct {
	mu        sync.Mutex
	byVehicle map[string][]RivoltAction
	now       func() time.Time // tests
}

func (l *actionLog) note(vehicleID, kind, op, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byVehicle == nil {
		l.byVehicle = map[string][]RivoltAction{}
	}
	at := time.Now()
	if l.now != nil {
		at = l.now()
	}
	ring := append(l.byVehicle[vehicleID], RivoltAction{At: at, Kind: kind, Op: op, Detail: detail})
	if len(ring) > actionRing {
		// Copy down rather than reslice so the backing array doesn't
		// grow without bound over a year of reconnects.
		ring = append(ring[:0], ring[len(ring)-actionRing:]...)
	}
	l.byVehicle[vehicleID] = ring
}

// around returns the vehicle's and the account's actions in [from, to],
// oldest first.
func (l *actionLog) around(vehicleID string, from, to time.Time) []RivoltAction {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []RivoltAction
	for _, key := range []string{vehicleID, ""} {
		for _, a := range l.byVehicle[key] {
			if !a.At.Before(from) && !a.At.After(to) {
				out = append(out, a)
			}
		}
		if vehicleID == "" {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// noteAction records a request this client made for vehicleID ("" for
// account-level calls).
func (c *LiveClient) noteAction(vehicleID, kind, op, detail string) {
	c.actions.note(vehicleID, kind, op, detail)
}

// RecentActions returns the vehicle's (and account-level) requests in
// [from, to], oldest first.
func (c *LiveClient) RecentActions(vehicleID string, from, to time.Time) []RivoltAction {
	return c.actions.around(vehicleID, from, to)
}

type actionReasonKey struct{}

// withActionReason tags the Rivian request made under ctx with why it
// was made; it shows as the action's detail in wake attribution
// (e.g. "request GetVehicleState(periodic_refresh)").
func withActionReason(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, actionReasonKey{}, reason)
}

func actionReason(ctx context.Context) string {
	s, _ := ctx.Value(actionReasonKey{}).(string)
	return s
}

// vehicleOf pulls the vehicle id out of GraphQL variables, whichever
// casing the operation uses; "" when there's none.
func vehicleOf(vars any) string {
	switch v := vars.(type) {
	case map[string]any:
		for _, k := range []string{"vehicleID", "vehicleId", "id"} {
			if s, ok := v[k].(string); ok && s != "" {
				return s
			}
		}
	case map[string]string:
		for _, k := range []string{"vehicleID", "vehicleId", "id"} {
			if s := v[k]; s != "" {
				return s
			}
		}
	}
	return ""
}

// topicsDetail summarises a subscription's topics for the action log:
// the names when there are one or two, else the count.
func topicsDetail(vars map[string]any) string {
	rvms, ok := vars["rvms"].([]string)
	if !ok || len(rvms) == 0 {
		return ""
	}
	if len(rvms) <= 2 {
		return strings.Join(rvms, ",")
	}
	return fmt.Sprintf("%d topics", len(rvms))
}

// Attribution window: actions this long before the wake are listed;
// a few seconds after are listed too, marked +, since a reconnect
// right after a wake is our reaction, not its cause.
const (
	wakeLookback  = 2 * time.Minute
	wakeLookahead = 5 * time.Second
	// wakeAnchorSlack bounds how far the car's reported wake time may
	// sit outside [sleep seen, wake seen] before it's distrusted
	// (clock skew, or a replayed stale timestamp).
	wakeAnchorSlack = time.Minute
	// lastActionHorizon: older than this, no action counts as "last".
	lastActionHorizon = 30 * time.Minute
)

// logWakeAttribution logs the requests around a sleep → awake
// transition the sleep gate saw at observed (the car was seen asleep
// from sleptSeen).
func (m *StateMonitor) logWakeAttribution(vehicleID string, sleptSeen, observed time.Time, wokeTo string, failed bool) {
	if m.client == nil {
		return
	}
	m.mu.RLock()
	var carAt time.Time
	if st := m.cache[vehicleID]; st != nil {
		carAt = st.PowerStateAt
	}
	m.mu.RUnlock()

	anchor, source := observed, "observed"
	if !carAt.IsZero() && carAt.After(sleptSeen.Add(-wakeAnchorSlack)) && carAt.Before(observed.Add(wakeAnchorSlack)) {
		anchor, source = carAt, "car"
	}
	before := m.client.RecentActions(vehicleID, anchor.Add(-lastActionHorizon), anchor)
	window := m.client.RecentActions(vehicleID, anchor.Add(-wakeLookback), anchor.Add(wakeLookahead))
	parts := make([]string, 0, len(window))
	for _, a := range window {
		d := a.At.Sub(anchor).Round(100 * time.Millisecond)
		sign := ""
		if d > 0 {
			sign = "+"
		}
		parts = append(parts, fmt.Sprintf("%s %s%s", a, sign, d))
	}
	attrs := []any{"vehicle", vehicleID, "woke_to", wokeTo, "failed", failed,
		"slept", observed.Sub(sleptSeen).Round(time.Second).String(),
		"wake_at", anchor.UTC().Format(time.RFC3339Nano), "wake_source", source,
		"seen_after", observed.Sub(anchor).Round(100 * time.Millisecond).String(),
		"actions_2m", strings.Join(parts, "; ")}
	if n := len(before); n > 0 {
		last := before[n-1]
		attrs = append(attrs, "last_action", last.String(),
			"last_action_before", anchor.Sub(last.At).Round(100*time.Millisecond).String())
	} else {
		attrs = append(attrs, "last_action", "none in 30m")
	}
	m.logger.Info("wake attribution", attrs...)
}
