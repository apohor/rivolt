package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// nowLookback bounds how far back the current state's run is searched.
// A state older than this reports state_since at the window edge.
const nowLookback = 7 * 24 * time.Hour

// nowSleepWindow is the window failed_sleeps_6h counts over.
const nowSleepWindow = 6 * time.Hour

// nowSnapshot is what the store derives for a vehicle at one instant.
type nowSnapshot struct {
	At            *time.Time
	LocationFixAt *time.Time
	State         string // driving|charging|plugged|asleep|parked|unknown
	// Asleep is true when the newest row's power_state is "sleep",
	// independent of State (a plugged truck can be asleep: State reads
	// "plugged" but its frames arrive every ~3 min, not every few s).
	Asleep           bool
	StateSince       *time.Time
	SoCAtStateStart  *float64
	TirePressureMinB *float64
	FailedSleeps6h   int
	ChargePeakKW     *float64
	// lat, lon, hasPos: the newest row's position (never serialised).
	lat, lon float64
	hasPos   bool
}

type nowCharge struct {
	StartedAt time.Time `json:"started_at"`
	SoCStart  *float64  `json:"soc_start"`
	PeakKW    *float64  `json:"peak_kw"`
}

type nowResponse struct {
	Version          int        `json:"version"`
	At               *time.Time `json:"at"`
	LocationFixAt    *time.Time `json:"location_fix_at"`
	Connection       string     `json:"connection"`
	State            string     `json:"state"`
	Asleep           bool       `json:"asleep"`
	StateSince       *time.Time `json:"state_since"`
	SoCAtStateStart  *float64   `json:"soc_at_state_start"`
	TirePressureMinB *float64   `json:"tire_pressure_min_bar"`
	FailedSleeps6h   int        `json:"failed_sleeps_6h"`
	// VehicleStateFrameAt is when the vehicleState feed last delivered
	// a frame; null when there's no live feed to judge (not streaming
	// yet, held while asleep, paused, reauth). An asleep truck's feed
	// still delivers every few minutes, so the client can hold it to
	// a tighter limit than the age of `at`.
	VehicleStateFrameAt *time.Time `json:"vehicle_state_frame_at"`
	Charge              *nowCharge `json:"charge"`
	// Ambient is the outside temperature looked up from Open-Meteo for the
	// truck's location rounded to about 11 km. Rivian reports none. Present only
	// when the user turned the lookup on (off by default) and the truck has a
	// position; never in the demo.
	Ambient *nowAmbient `json:"ambient,omitempty"`
}

// nowAmbient is the looked-up outside temperature.
type nowAmbient struct {
	TempC  float64   `json:"temp_c"`
	Source string    `json:"source"` // "open-meteo"
	AsOf   time.Time `json:"as_of"`  // the hour the value is for (UTC)
}

// nowAPI holds the /now handler's seams so tests run without Postgres.
type nowAPI struct {
	snapshot   func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) (nowSnapshot, error)
	connection func(uid uuid.UUID) string
	// frameAt reads the live feed's last vehicleState frame time
	// (rivian.StateMonitor.VehicleStateFrameAt); nil leaves it null.
	frameAt func(ctx context.Context, uid uuid.UUID, rivianID string) *time.Time
	// ambient looks up the outside temperature at (lat, lon) for a user who opted
	// in; nil (the seam, or the user's choice) leaves the field out. It must not
	// log or return the coordinates.
	ambient func(ctx context.Context, uid uuid.UUID, lat, lon float64) *nowAmbient
	now     func() time.Time
}

// connectionState is not a freshness verdict (the client derives that
// from `at` and its per-state cadence): replay > paused > reauth > live.
func connectionState(replay, paused, needsReauth bool) string {
	switch {
	case replay:
		return "replay"
	case paused:
		return "paused"
	case needsReauth:
		return "reauth"
	}
	return "live"
}

// handle serves GET /api/vehicles/{vehicleID}/now. The route is
// vehicleScoped (ownership).
func (a nowAPI) handle(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "vehicleID"))
	if id == "" {
		http.Error(w, "vehicleID required", http.StatusBadRequest)
		return
	}
	snap, err := a.snapshot(r.Context(), uid, id, a.now())
	if err != nil {
		http.Error(w, "now: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := nowResponse{
		Version: 1, At: snap.At, LocationFixAt: snap.LocationFixAt,
		Connection: a.connection(uid), State: snap.State, Asleep: snap.Asleep, StateSince: snap.StateSince,
		SoCAtStateStart: snap.SoCAtStateStart, TirePressureMinB: snap.TirePressureMinB,
		FailedSleeps6h: snap.FailedSleeps6h,
	}
	if out.State == "" {
		out.State = "unknown"
	}
	if a.frameAt != nil && (out.Connection == "live" || out.Connection == "replay") {
		out.VehicleStateFrameAt = a.frameAt(r.Context(), uid, id)
	}
	if out.State == "charging" && snap.StateSince != nil {
		out.Charge = &nowCharge{StartedAt: *snap.StateSince, SoCStart: snap.SoCAtStateStart, PeakKW: snap.ChargePeakKW}
	}
	if a.ambient != nil && snap.hasPos {
		out.Ambient = a.ambient(r.Context(), uid, snap.lat, snap.lon)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, out)
}

// envReplay reports whether this install runs the replay client.
func envReplay() bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv("RIVIAN_CLIENT")), "replay")
}

// nowStateCase classifies one vehicle_state row (alias vs). Precedence:
// driving (gear D/R only) > charging (charging_active) > plugged
// (connected, not charging) > asleep (power_state) > parked. A plugged
// truck that is also asleep reads "plugged" (agreed with ux).
//
// Plugged:
//   - a row with a charger_status (R1s report it, migration 0053) is
//     plugged when it starts with chrgr_sts_connected, whatever the
//     charging_state says: an R1 reports charging_ready while parked
//     unplugged;
//   - a row without one on an R1 (older rows) keeps the charging_state
//     rule (ready, complete or connecting);
//   - an R2 never reports charger_status, and its charging_ready (and
//     charging_complete) say nothing about the plug, so the only sign is
//     the recorder's own: charging_connecting with power flowing. This is
//     the rule the recorder uses to open a charge (liveSessions.plugged).
//
// The query's v CTE supplies r2 (vehicles.model).
const nowStateCase = `CASE WHEN %[1]s.shift_state IN ('D','R') THEN 'driving'
	            WHEN %[1]s.charging_state = 'charging_active' THEN 'charging'
	            WHEN starts_with(COALESCE(%[1]s.charger_status, ''), 'chrgr_sts_connected') THEN 'plugged'
	            WHEN COALESCE(%[1]s.charger_status, '') = '' AND NOT COALESCE((SELECT r2 FROM v), false)
	                 AND %[1]s.charging_state IN ('charging_ready','charging_complete','charging_connecting') THEN 'plugged'
	            WHEN COALESCE(%[1]s.charger_status, '') = '' AND COALESCE((SELECT r2 FROM v), false)
	                 AND %[1]s.charging_state = 'charging_connecting' AND COALESCE(%[1]s.charger_power_kw, 0) > 0 THEN 'plugged'
	            WHEN %[1]s.power_state = 'sleep' THEN 'asleep'
	            ELSE 'parked' END`

// nowCurSQL reads the newest row's state, fix time and the newest tire
// pressure in the last 6 h: two LIMIT 1 index probes, cheap every poll.
var nowCurSQL = fmt.Sprintf(`
WITH v AS (
	SELECT id, (upper(COALESCE(model, '')) = 'R2') AS r2 FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
), cur AS (
	SELECT vs.at, vs.location_fix_at, `+nowStateCase+` AS st,
	       (vs.power_state IS NOT DISTINCT FROM 'sleep') AS asleep,
	       vs.lat, vs.lon
	FROM vehicle_state vs
	WHERE vs.user_id = $1 AND vs.vehicle_id = (SELECT id FROM v) AND vs.at >= $3::timestamptz
	ORDER BY vs.at DESC LIMIT 1
)
SELECT (SELECT at FROM cur),
       (SELECT st FROM cur),
       (SELECT location_fix_at FROM cur),
       (SELECT asleep FROM cur),
       (SELECT vs.tire_pressure_min_bar FROM vehicle_state vs
         WHERE vs.user_id = $1 AND vs.vehicle_id = (SELECT id FROM v)
           AND vs.tire_pressure_min_bar IS NOT NULL AND vs.at >= $4::timestamptz
         ORDER BY vs.at DESC LIMIT 1),
       (SELECT lat FROM cur),
       (SELECT lon FROM cur)`, "vs")

// nowRunSQL finds where the current run ($4 = its state) began: an
// early-stopping descending scan on the (vehicle_id, at) key that stops
// at the first row whose state differs. This is the part worth
// caching; with no differing row inside the lookback the state is older
// than the window and the start is NULL (unknown), not the window edge.
//
// The rows after the break are bounded by the lookback ($3) as well as by
// the break: brk.at is never before $3, so the meaning is the same, but
// the explicit bound lets Postgres skip the older monthly partitions, and
// comparing with (SELECT at FROM brk) rather than joining brk keeps the
// break time an index bound. Joined, it was neither: every call read the
// vehicle's whole history in every partition (about 1 GB a call for a
// year-old truck, Oct 24, 2026).
var nowRunSQL = fmt.Sprintf(`
WITH v AS (
	SELECT id, (upper(COALESCE(model, '')) = 'R2') AS r2 FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
), brk AS (
	SELECT vs.at
	FROM vehicle_state vs
	WHERE vs.user_id = $1 AND vs.vehicle_id = (SELECT id FROM v) AND vs.at >= $3::timestamptz
	  AND `+nowStateCase+` <> $4::text
	ORDER BY vs.at DESC LIMIT 1
), first AS (
	SELECT vs.at, vs.battery_level_pct
	FROM vehicle_state vs
	WHERE vs.user_id = $1 AND vs.vehicle_id = (SELECT id FROM v)
	  AND vs.at >= $3::timestamptz AND vs.at > (SELECT at FROM brk)
	ORDER BY vs.at ASC LIMIT 1
)
SELECT (SELECT at FROM first),
       (SELECT battery_level_pct FROM first),
       (SELECT max(vs.charger_power_kw) FROM vehicle_state vs
         WHERE vs.user_id = $1 AND vs.vehicle_id = (SELECT id FROM v)
           AND vs.at >= $3::timestamptz AND vs.at > (SELECT at FROM brk))`, "vs")

// nowSleepSQL counts sleeps that ended within a minute in the window,
// with the same definition as the sleep-activity analytics: a sleep is
// the first 'sleep' sample after a non-sleep one, ending at the next
// non-sleep sample; one still in progress is not counted.
const nowSleepSQL = `
WITH s AS (
	SELECT vs.at, vs.power_state,
	       LAG(vs.power_state) OVER (ORDER BY vs.at) AS prev_ps
	FROM vehicle_state vs
	WHERE vs.user_id = $1
	  AND vs.vehicle_id = (SELECT v.id FROM vehicles v WHERE v.user_id = $1 AND v.rivian_vehicle_id = $2)
	  AND vs.at >= $3::timestamptz AND vs.power_state IS NOT NULL
), changes AS (
	SELECT at, power_state, LEAD(at) OVER (ORDER BY at) AS next_change_at
	FROM s WHERE power_state IS DISTINCT FROM prev_ps
)
SELECT COUNT(*) FROM changes
WHERE power_state = 'sleep' AND next_change_at IS NOT NULL
  AND next_change_at - at < interval '60 seconds'`

// nowRun is the cached, expensive part of a snapshot: where the
// current state's run began, its start SoC and peak kW, and the 6 h
// failed-sleep count.
type nowRun struct {
	state   string
	since   *time.Time
	soc     *float64
	peak    *float64
	failed  int
	expires time.Time
}

// nowRunCache holds nowRun per (user, vehicle) for nowRunTTL. An entry
// is valid only while the vehicle is still in the state it was
// computed for, so a state change invalidates it immediately; the TTL
// bounds staleness of peak kW and the sleep count.
type nowRunCache struct {
	mu  sync.Mutex
	m   map[string]nowRun
	ttl time.Duration
}

const nowRunTTL = 60 * time.Second

func newNowRunCache(ttl time.Duration) *nowRunCache {
	return &nowRunCache{m: map[string]nowRun{}, ttl: ttl}
}

func nowCacheKey(uid uuid.UUID, rivianID string) string { return uid.String() + "|" + rivianID }

func (c *nowRunCache) get(uid uuid.UUID, rivianID, state string, now time.Time) (nowRun, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.m[nowCacheKey(uid, rivianID)]
	if !ok || r.state != state || !now.Before(r.expires) {
		return nowRun{}, false
	}
	return r, true
}

func (c *nowRunCache) put(uid uuid.UUID, rivianID string, r nowRun, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) > 1024 {
		for k, v := range c.m {
			if !now.Before(v.expires) {
				delete(c.m, k)
			}
		}
	}
	r.expires = now.Add(c.ttl)
	c.m[nowCacheKey(uid, rivianID)] = r
}

// newNowSnapshotter is the production seam: a cheap current-row query
// every poll, plus the run/sleep queries cached per vehicle.
func newNowSnapshotter(pool *sql.DB) func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
	return newCachedNowSnapshotter(pool, newNowRunCache(nowRunTTL))
}

func newCachedNowSnapshotter(pool *sql.DB, cache *nowRunCache) func(context.Context, uuid.UUID, string, time.Time) (nowSnapshot, error) {
	return func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) (nowSnapshot, error) {
		var out nowSnapshot
		var at, fix sql.NullTime
		var st sql.NullString
		var tire, lat, lon sql.NullFloat64
		var asleep sql.NullBool
		sleepFrom := now.Add(-nowSleepWindow).UTC()
		if err := pool.QueryRowContext(ctx, nowCurSQL, uid, rivianID, now.Add(-nowLookback).UTC(), sleepFrom).
			Scan(&at, &st, &fix, &asleep, &tire, &lat, &lon); err != nil {
			return out, err
		}
		if at.Valid {
			t := at.Time.UTC()
			out.At = &t
		}
		if fix.Valid {
			t := fix.Time.UTC()
			out.LocationFixAt = &t
		}
		if tire.Valid {
			out.TirePressureMinB = &tire.Float64
		}
		// The position stays inside the server: it is only ever used to look up
		// the ambient temperature (when the user opted in) and is never returned.
		if lat.Valid && lon.Valid && (lat.Float64 != 0 || lon.Float64 != 0) {
			out.lat, out.lon, out.hasPos = lat.Float64, lon.Float64, true
		}
		out.State = st.String
		out.Asleep = asleep.Valid && asleep.Bool
		if !st.Valid {
			return out, nil // no rows in the lookback
		}
		run, ok := cache.get(uid, rivianID, out.State, now)
		if !ok {
			var since sql.NullTime
			var soc, peak sql.NullFloat64
			if err := pool.QueryRowContext(ctx, nowRunSQL, uid, rivianID, now.Add(-nowLookback).UTC(), out.State).
				Scan(&since, &soc, &peak); err != nil {
				return out, err
			}
			run = nowRun{state: out.State}
			if since.Valid {
				t := since.Time.UTC()
				run.since = &t
			}
			if soc.Valid {
				run.soc = &soc.Float64
			}
			if peak.Valid && peak.Float64 > 0 {
				run.peak = &peak.Float64
			}
			if err := pool.QueryRowContext(ctx, nowSleepSQL, uid, rivianID, sleepFrom).Scan(&run.failed); err != nil {
				return out, err
			}
			cache.put(uid, rivianID, run, now)
		}
		out.StateSince, out.SoCAtStateStart, out.ChargePeakKW, out.FailedSleeps6h = run.since, run.soc, run.peak, run.failed
		return out, nil
	}
}
