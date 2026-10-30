package rivian

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"
)

// The vehicle's charging schedules, as the owner app reads them
// (GetChargingSchedule on the consumer gateway). Read-only:
// SetChargingSchedule is never called. A schedule is a daily window
// (startTime and duration in minutes after local midnight, on the listed
// weekDays) at a location: the app stamps the truck's position when the
// schedule is saved, and the vehicle applies it only there.
//
// The location is used for one thing, AppliesHere (is the truck at the
// schedule's place), and is kept in unexported fields: it is never
// serialized, published to peers, stored or logged.
const qChargingSchedules = `query GetChargingSchedule($vehicleId: String!) {
  getVehicle(id: $vehicleId) {
    chargingSchedules { startTime duration location { latitude longitude } amperage enabled weekDays }
  }
}`

// ChargingSchedule is one scheduled charging window.
type ChargingSchedule struct {
	// StartMin is the window start in minutes after local midnight.
	StartMin int `json:"start_min"`
	// DurationMin is the window length in minutes (1440 = all day).
	DurationMin int  `json:"duration_min"`
	Enabled     bool `json:"enabled"`
	// WeekDays are the days the window starts on, "Monday".."Sunday".
	WeekDays []string `json:"weekdays"`
	// AmperageA is the charge current the schedule sets.
	AmperageA int `json:"amperage_a,omitempty"`
	// AppliesHere reports whether the truck was at the schedule's location
	// (within scheduleRadiusM) at the last read. Reads happen on connect,
	// daily, and when the truck is seen plugged in and not charging, so
	// it's current whenever that's the question. nil when either position
	// is unknown.
	AppliesHere *bool `json:"applies_here,omitempty"`

	lat, lon float64
	hasLoc   bool
}

// scheduleRadiusM is how close the truck must be to a schedule's location
// for it to apply: a driveway and the street in front of it, not the
// neighbourhood.
const scheduleRadiusM = 250

type chargingSchedulesData struct {
	GetVehicle struct {
		ChargingSchedules []struct {
			StartTime int `json:"startTime"`
			Duration  int `json:"duration"`
			Location  *struct {
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
			} `json:"location"`
			Amperage int      `json:"amperage"`
			Enabled  bool     `json:"enabled"`
			WeekDays []string `json:"weekDays"`
		} `json:"chargingSchedules"`
	} `json:"getVehicle"`
}

// ChargingSchedules reads the vehicle's charging schedules. An empty list
// is a valid answer (no schedule set).
func (c *LiveClient) ChargingSchedules(ctx context.Context, vehicleID string) ([]ChargingSchedule, error) {
	headers, err := c.sessionHeaders()
	if err != nil {
		return nil, err
	}
	data, err := doGraphQLDetached[chargingSchedulesData](ctx, c, graphQLRequest{
		OperationName: "GetChargingSchedule",
		Query:         qChargingSchedules,
		Variables:     map[string]any{"vehicleId": vehicleID},
	}, headers)
	if err != nil {
		return nil, fmt.Errorf("GetChargingSchedule: %w", err)
	}
	out := make([]ChargingSchedule, 0, len(data.GetVehicle.ChargingSchedules))
	for _, r := range data.GetVehicle.ChargingSchedules {
		s := ChargingSchedule{
			StartMin: r.StartTime, DurationMin: r.Duration, Enabled: r.Enabled,
			WeekDays: r.WeekDays, AmperageA: r.Amperage,
		}
		// The app writes 0,0 when it had no position; that's no location.
		if r.Location != nil && (r.Location.Latitude != 0 || r.Location.Longitude != 0) {
			s.lat, s.lon, s.hasLoc = r.Location.Latitude, r.Location.Longitude, true
		}
		out = append(out, s)
	}
	return out, nil
}

// withAppliesHere returns a copy of the schedules with AppliesHere set
// against the truck's position (lat, lon; 0,0 = unknown).
func withAppliesHere(in []ChargingSchedule, lat, lon float64) []ChargingSchedule {
	out := make([]ChargingSchedule, len(in))
	for i, s := range in {
		s.AppliesHere = nil
		if s.hasLoc && (lat != 0 || lon != 0) {
			here := haversineM(lat, lon, s.lat, s.lon) <= scheduleRadiusM
			s.AppliesHere = &here
		}
		out[i] = s
	}
	return out
}

// haversineM is the great-circle distance in metres.
func haversineM(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371000.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// pluggedIdle reports whether a state reads as plugged in and not
// charging: the case a schedule explains.
func pluggedIdle(s *State) bool {
	if s == nil || chargerDrawing(s.ChargerState) {
		return false
	}
	return isPluggedCS(s.ChargerStatus) || strings.EqualFold(strings.TrimSpace(s.ChargerState), "charging_scheduled")
}

// Schedule read cadence. Connect reads are skipped when the last read is
// fresher than scheduleConnectMinAge (reconnects are frequent); plug-in
// nudges are skipped within scheduleNudgeMinAge unless the truck moved.
const (
	scheduleDaily         = 24 * time.Hour
	scheduleConnectMinAge = time.Hour
	scheduleNudgeMinAge   = 30 * time.Minute
	// scheduleUnavailableBackoff is how long a vehicle whose schedules Rivian
	// won't share (FORBIDDEN: a shared or authorized-driver account) is left
	// alone before one more try.
	scheduleUnavailableBackoff = 7 * 24 * time.Hour
)

type scheduleRead struct {
	at       time.Time
	lat, lon float64
}

// scheduleConnectJitter spreads the connect-time reads of a pod that has just taken several vehicles
// over; scheduleRetryCap and scheduleRetryJitter bound the one retry after a rate-limit refusal.
// Vars so tests can shrink them.
var (
	scheduleConnectJitter = 2 * time.Second
	scheduleRetryCap      = 5 * time.Second
	scheduleRetryJitter   = 500 * time.Millisecond
)

// jitterUpTo is a random duration in [0, max).
func jitterUpTo(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int63n(int64(max)))
}

// chargingScheduleLoop keeps the cached state's ChargingSchedules current
// for one vehicle: a read on connect, then daily, and on a nudge (the
// truck seen plugged in and idle; see noteScheduleNudge). Errors keep the
// last good list.
func (m *StateMonitor) chargingScheduleLoop(ctx context.Context, vehicleID string) {
	nudge := m.scheduleNudgeChan(vehicleID)
	// A lease handover starts every vehicle of the pod at once: spread the first reads over a couple of
	// seconds instead of sending them as one burst into the shared Rivian budget.
	if !sleepCtx(ctx, jitterUpTo(scheduleConnectJitter)) {
		return
	}
	m.readChargingSchedules(ctx, vehicleID, scheduleConnectMinAge)
	t := time.NewTicker(scheduleDaily)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.readChargingSchedules(ctx, vehicleID, 0)
		case <-nudge:
			m.readChargingSchedules(ctx, vehicleID, scheduleNudgeMinAge)
		}
	}
}

func (m *StateMonitor) scheduleNudgeChan(vehicleID string) chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scheduleNudge == nil {
		m.scheduleNudge = map[string]chan struct{}{}
	}
	ch := m.scheduleNudge[vehicleID]
	if ch == nil {
		ch = make(chan struct{}, 1)
		m.scheduleNudge[vehicleID] = ch
	}
	return ch
}

// noteScheduleNudge asks for a schedule read when the truck turns plugged
// in and idle. Non-blocking; a pending nudge absorbs the rest.
func (m *StateMonitor) noteScheduleNudge(vehicleID string, prev, curr *State) {
	if pluggedIdle(prev) || !pluggedIdle(curr) {
		return
	}
	m.mu.RLock()
	ch := m.scheduleNudge[vehicleID]
	m.mu.RUnlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// readChargingSchedules reads and caches the schedules, unless the last
// read is younger than minAge and the truck hasn't moved since.
func (m *StateMonitor) readChargingSchedules(ctx context.Context, vehicleID string, minAge time.Duration) {
	fetch := m.fetchSchedules
	if fetch == nil {
		if m.client == nil {
			return
		}
		fetch = m.client.ChargingSchedules
	}
	m.mu.RLock()
	until := m.scheduleUnavailableUntil[vehicleID]
	last := m.scheduleReads[vehicleID]
	var lat, lon float64
	if st := m.cache[vehicleID]; st != nil {
		lat, lon = st.Latitude, st.Longitude
	}
	m.mu.RUnlock()
	if time.Now().Before(until) {
		return
	}
	if minAge > 0 && !last.at.IsZero() && time.Since(last.at) < minAge &&
		haversineM(lat, lon, last.lat, last.lon) <= scheduleRadiusM {
		return
	}
	list, err := fetch(ctx, vehicleID)
	var rl *ErrRateLimited
	if errors.As(err, &rl) && ctx.Err() == nil {
		// The shared budget was empty at that moment (a handover burst): wait the time it says, once.
		wait := min(rl.RetryAfter, scheduleRetryCap) + jitterUpTo(scheduleRetryJitter)
		if sleepCtx(ctx, wait) {
			list, err = fetch(ctx, vehicleID)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if IsNotAvailable(err) {
			// Expected for shared and authorized-driver accounts: mark the
			// vehicle "not available" and back off a week, at info level.
			m.mu.Lock()
			if m.scheduleUnavailableUntil == nil {
				m.scheduleUnavailableUntil = map[string]time.Time{}
			}
			m.scheduleUnavailableUntil[vehicleID] = time.Now().Add(scheduleUnavailableBackoff)
			if base := m.cache[vehicleID]; base != nil {
				next := *base
				next.ChargingSchedules = nil
				next.ChargingSchedulesUnavailable = true
				m.putCacheLocked(vehicleID, &next)
			}
			m.mu.Unlock()
			m.logger.Info("charging schedules not available for this account", "vehicle", vehicleID, "code", ErrCode(err))
			return
		}
		m.logger.Warn("charging schedules read failed", "vehicle", vehicleID, "err", Describe(err))
		return
	}
	enabled := 0
	for _, s := range list {
		if s.Enabled {
			enabled++
		}
	}
	m.mu.Lock()
	// No cached state yet: nothing to attach to, and no read recorded, so
	// the next nudge or the daily tick reads again.
	if base := m.cache[vehicleID]; base != nil {
		if m.scheduleReads == nil {
			m.scheduleReads = map[string]scheduleRead{}
		}
		m.scheduleReads[vehicleID] = scheduleRead{at: time.Now(), lat: lat, lon: lon}
		next := *base
		next.ChargingSchedulesUnavailable = false
		next.ChargingSchedules = withAppliesHere(list, base.Latitude, base.Longitude)
		m.putCacheLocked(vehicleID, &next)
	}
	m.mu.Unlock()
	m.logger.Info("charging schedules read", "vehicle", vehicleID, "schedules", len(list), "enabled", enabled)
}
