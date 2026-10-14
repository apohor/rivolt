package api

import (
	"context"
	"database/sql"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// Parked-drain endpoint (B8, scratchpad/drain-contract.md). Per-vehicle,
// per-user, computed from vehicle_state only (never Rivian, so it can't
// wake a car). Definitions are the D1 gauges' (internal/drain) and
// devils-advocate's D3 method:
//
//   - drain pair: consecutive samples both parked (gear P or unknown,
//     under 1 mph, not charging), under 24 h apart, odometer under
//     0.2 mi, |dSoC| under 3; drain = 24 * sum(dSoC) / sum(hours).
//   - idle awake: hours awake (power_state <> sleep) while parked and
//     not charging, per 24 h of observed power state; intervals capped
//     at 20 min.
//   - sleep: first 'sleep' sample after a non-sleep one to the next
//     non-sleep sample; quick re-wake = under 60 s; counted in the
//     window / local day it ENDED in.
//
// Windows are the rolling 7 x 24 h and 30 x 24 h, so the rate equals the
// D1 gauge. daily rows are local days in tz.

const (
	drainDays            = 30
	drainMinParkedH7d    = 24.0
	drainMinParkedH30d   = 72.0
	// A day's bar needs 12 h of valid parked pairs: on 3 h prod days
	// swung from -0.9 to 7.3 %/day. The window rate still uses every
	// pair; this only gates the per-day bar.
	drainMinDailyParkedH = 12.0
	// A day's idle-awake figure needs this much observed power state.
	drainMinDailyObsH = 3.0
	drainCacheTTL        = 60 * time.Second
)

type drainWindow struct {
	DrainPctPerDay        *float64       `json:"drain_pct_per_day"`
	DrainKWhPerDay        *float64       `json:"drain_kwh_per_day"`
	IdleAwakeHoursPerDay  *float64       `json:"idle_awake_hours_per_day"`
	Sleeps                int            `json:"sleeps"`
	QuickRewakes          int            `json:"quick_rewakes"`
	MedianSleepSeconds    *float64       `json:"median_sleep_seconds"`
	ParkedHours           float64        `json:"parked_hours"`
	PowerStateObservedHrs float64        `json:"power_state_observed_hours"`
	DaysInWindow          int            `json:"days_in_window"`
	DaysWithData          int            `json:"days_with_data"`
	DaysExcluded          map[string]int `json:"days_excluded"`
	Sufficient            bool           `json:"sufficient"`
}

type drainDay struct {
	Day            string   `json:"day"`
	DrainPct       *float64 `json:"drain_pct"`
	IdleAwakeHours *float64 `json:"idle_awake_hours"`
	ParkedHours    float64  `json:"parked_hours"`
	ChargingHours  float64  `json:"charging_hours"`
	DrivingHours   float64  `json:"driving_hours"`
	Sleeps         int      `json:"sleeps"`
	QuickRewakes   int      `json:"quick_rewakes"`
	Excluded       *string  `json:"excluded"`
}

type drainResponse struct {
	Version     int                    `json:"version"`
	GeneratedAt time.Time              `json:"generated_at"`
	TZ          string                 `json:"tz"`
	PackKWh     *float64               `json:"pack_kwh"`
	Windows     map[string]drainWindow `json:"windows"`
	Daily       []drainDay             `json:"daily"`
}

// drainAgg is one scope's (a local day, or a rolling window) sums.
type drainAgg struct {
	ParkH, DropPct, ObsH, AwakeH, ChargingH, DrivingH float64
	Sleeps, Quick                                     int
	MedianSleepS                                      *float64
}

// drainRaw is what the store returns; buildDrain turns it into the
// response.
type drainRaw struct {
	PackKWh *float64
	Days    map[string]drainAgg // keyed by local YYYY-MM-DD
	W7, W30 drainAgg
}

func drainRound(v float64) float64 { return math.Round(v*100) / 100 }

func drainRate(drop, park float64) float64 { return 24 * drop / park }

// buildDrain is pure: raw sums plus the clock and zone -> response.
func buildDrain(raw drainRaw, now time.Time, loc *time.Location) drainResponse {
	out := drainResponse{
		Version: 1, GeneratedAt: now.UTC(), TZ: loc.String(), PackKWh: raw.PackKWh,
		Windows: map[string]drainWindow{}, Daily: make([]drainDay, 0, drainDays),
	}
	today := now.In(loc)
	for i := drainDays - 1; i >= 0; i-- {
		d := today.AddDate(0, 0, -i)
		key := d.Format("2006-01-02")
		a := raw.Days[key]
		row := drainDay{
			Day: key, ParkedHours: drainRound(a.ParkH), ChargingHours: drainRound(a.ChargingH),
			DrivingHours: drainRound(a.DrivingH), Sleeps: a.Sleeps, QuickRewakes: a.Quick,
		}
		if a.ParkH >= drainMinDailyParkedH {
			v := drainRound(drainRate(a.DropPct, a.ParkH))
			row.DrainPct = &v
		} else {
			ex := "insufficient"
			row.Excluded = &ex
		}
		if a.ObsH >= drainMinDailyObsH {
			v := drainRound(24 * a.AwakeH / a.ObsH)
			row.IdleAwakeHours = &v
		}
		out.Daily = append(out.Daily, row)
	}
	mk := func(a drainAgg, days int, minParked float64) drainWindow {
		w := drainWindow{
			Sleeps: a.Sleeps, QuickRewakes: a.Quick, MedianSleepSeconds: a.MedianSleepS,
			ParkedHours: drainRound(a.ParkH), PowerStateObservedHrs: drainRound(a.ObsH),
			DaysInWindow: days, DaysExcluded: map[string]int{"insufficient": 0},
			Sufficient: a.ParkH >= minParked,
		}
		for _, r := range out.Daily[len(out.Daily)-days:] {
			if r.ParkedHours > 0 || r.IdleAwakeHours != nil || r.Sleeps > 0 {
				w.DaysWithData++
			}
			if r.Excluded != nil {
				w.DaysExcluded["insufficient"]++
			}
		}
		if w.Sufficient {
			v := drainRound(drainRate(a.DropPct, a.ParkH))
			w.DrainPctPerDay = &v
			if raw.PackKWh != nil {
				k := drainRound(v * *raw.PackKWh / 100)
				w.DrainKWhPerDay = &k
			}
		}
		if a.ObsH >= minParked {
			v := drainRound(24 * a.AwakeH / a.ObsH)
			w.IdleAwakeHoursPerDay = &v
		}
		return w
	}
	out.Windows["7d"] = mk(raw.W7, 7, drainMinParkedH7d)
	out.Windows["30d"] = mk(raw.W30, 30, drainMinParkedH30d)
	return out
}

// drainIntervalsSQL aggregates intervals per local day ($5 = IANA zone)
// and over the rolling 7 d ($6) and 30 d ($7) windows. $1 user, $2
// Rivian vehicle id, $3 scan start, $4 now. Scoped by user_id.
const drainIntervalsSQL = `
WITH v AS (
	SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
), s AS (
	SELECT at, battery_level_pct AS soc, odometer_mi AS odo, power_state AS ps,
	       shift_state AS gear, speed_mph AS mph, charging_state AS cs
	  FROM vehicle_state
	 WHERE user_id = $1 AND vehicle_id = (SELECT id FROM v)
	   AND at >= $3::timestamptz AND at < $4::timestamptz
), p AS (
	SELECT at, soc, odo,
	       LAG(at) OVER w AS pat, LAG(soc) OVER w AS psoc, LAG(odo) OVER w AS podo, LAG(ps) OVER w AS pps,
	       (COALESCE(gear,'') IN ('P','') AND COALESCE(mph,0) < 1
	        AND COALESCE(cs,'') NOT IN ('charging_active','charging_connecting')) AS idle,
	       LAG(COALESCE(gear,'') IN ('P','') AND COALESCE(mph,0) < 1
	           AND COALESCE(cs,'') NOT IN ('charging_active','charging_connecting')) OVER w AS pidle,
	       LAG(COALESCE(gear,'') NOT IN ('D','R','N')
	           AND COALESCE(cs,'') NOT IN ('charging_active','charging_connecting')) OVER w AS pparked,
	       LAG(cs IN ('charging_active','charging_connecting')) OVER w AS pcharging,
	       LAG(gear IN ('D','R')) OVER w AS pdriving
	  FROM s WINDOW w AS (ORDER BY at)
), iv AS (
	SELECT at, pps, pparked, pcharging, pdriving,
	       EXTRACT(EPOCH FROM at - pat) / 3600.0 AS h,
	       LEAST(EXTRACT(EPOCH FROM at - pat), 1200) / 3600.0 AS hc,
	       COALESCE(idle AND pidle AND at - pat < interval '24 hours' AND soc > 0 AND psoc > 0
	                AND abs(COALESCE(odo,0) - COALESCE(podo,0)) < 0.2 AND abs(soc - psoc) < 3, false) AS pair,
	       psoc - soc AS d
	  FROM p WHERE pat IS NOT NULL
)
SELECT 'd' AS k, to_char((at AT TIME ZONE $5::text)::date, 'YYYY-MM-DD') AS day,
       COALESCE(SUM(h) FILTER (WHERE pair), 0), COALESCE(SUM(d) FILTER (WHERE pair), 0),
       COALESCE(SUM(hc) FILTER (WHERE pps IS NOT NULL), 0),
       COALESCE(SUM(hc) FILTER (WHERE pps <> 'sleep' AND pparked), 0),
       COALESCE(SUM(hc) FILTER (WHERE pcharging), 0), COALESCE(SUM(hc) FILTER (WHERE pdriving), 0)
  FROM iv GROUP BY 2
UNION ALL
SELECT '7d', NULL,
       COALESCE(SUM(h) FILTER (WHERE pair), 0), COALESCE(SUM(d) FILTER (WHERE pair), 0),
       COALESCE(SUM(hc) FILTER (WHERE pps IS NOT NULL), 0),
       COALESCE(SUM(hc) FILTER (WHERE pps <> 'sleep' AND pparked), 0), 0, 0
  FROM iv WHERE at >= $6::timestamptz
UNION ALL
SELECT '30d', NULL,
       COALESCE(SUM(h) FILTER (WHERE pair), 0), COALESCE(SUM(d) FILTER (WHERE pair), 0),
       COALESCE(SUM(hc) FILTER (WHERE pps IS NOT NULL), 0),
       COALESCE(SUM(hc) FILTER (WHERE pps <> 'sleep' AND pparked), 0), 0, 0
  FROM iv WHERE at >= $7::timestamptz`

// drainSleepsSQL counts sleeps by the local day / window they ended in.
const drainSleepsSQL = `
WITH v AS (
	SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
), c AS (
	SELECT at, power_state AS ps, LAG(power_state) OVER (ORDER BY at) AS prev_ps
	  FROM vehicle_state
	 WHERE user_id = $1 AND vehicle_id = (SELECT id FROM v)
	   AND at >= $3::timestamptz AND at < $4::timestamptz AND power_state IS NOT NULL
	   AND power_state <> ''
), x AS (
	SELECT at, ps, prev_ps, LEAD(at) OVER (ORDER BY at) AS next_at
	  FROM c
	 WHERE ps IS DISTINCT FROM prev_ps
), sl AS (
	SELECT next_at, EXTRACT(EPOCH FROM next_at - at) AS dur
	  FROM x WHERE ps = 'sleep' AND prev_ps IS NOT NULL AND next_at IS NOT NULL
)
SELECT 'd', to_char((next_at AT TIME ZONE $5::text)::date, 'YYYY-MM-DD'),
       COUNT(*), COUNT(*) FILTER (WHERE dur < 60), NULL::float8
  FROM sl GROUP BY 2
UNION ALL
SELECT '7d', NULL, COUNT(*), COUNT(*) FILTER (WHERE dur < 60),
       percentile_cont(0.5) WITHIN GROUP (ORDER BY dur)
  FROM sl WHERE next_at >= $6::timestamptz
UNION ALL
SELECT '30d', NULL, COUNT(*), COUNT(*) FILTER (WHERE dur < 60),
       percentile_cont(0.5) WITHIN GROUP (ORDER BY dur)
  FROM sl WHERE next_at >= $7::timestamptz`

// drainStore loads the raw sums for one vehicle.
type drainStore func(ctx context.Context, uid uuid.UUID, rivianID string, loc *time.Location, now time.Time) (drainRaw, error)

func newDrainStore(pool *sql.DB) drainStore {
	return func(ctx context.Context, uid uuid.UUID, rivianID string, loc *time.Location, now time.Time) (drainRaw, error) {
		raw := drainRaw{Days: map[string]drainAgg{}}
		var pack sql.NullFloat64
		if err := pool.QueryRowContext(ctx,
			`SELECT pack_kwh FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2`, uid, rivianID).Scan(&pack); err != nil && err != sql.ErrNoRows {
			return raw, err
		}
		if pack.Valid && pack.Float64 > 0 {
			raw.PackKWh = &pack.Float64
		}
		now = now.UTC()
		from := now.Add(-(drainDays + 1) * 24 * time.Hour)
		args := []any{uid, rivianID, from, now, loc.String(), now.Add(-7 * 24 * time.Hour), now.Add(-30 * 24 * time.Hour)}

		rows, err := pool.QueryContext(ctx, drainIntervalsSQL, args...)
		if err != nil {
			return raw, err
		}
		for rows.Next() {
			var k string
			var day sql.NullString
			var a drainAgg
			if err := rows.Scan(&k, &day, &a.ParkH, &a.DropPct, &a.ObsH, &a.AwakeH, &a.ChargingH, &a.DrivingH); err != nil {
				rows.Close()
				return raw, err
			}
			switch k {
			case "7d":
				raw.W7 = a
			case "30d":
				raw.W30 = a
			default:
				raw.Days[day.String] = a
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return raw, err
		}
		rows.Close()

		srows, err := pool.QueryContext(ctx, drainSleepsSQL, args...)
		if err != nil {
			return raw, err
		}
		defer srows.Close()
		for srows.Next() {
			var k string
			var day sql.NullString
			var n, q int
			var med sql.NullFloat64
			if err := srows.Scan(&k, &day, &n, &q, &med); err != nil {
				return raw, err
			}
			var mp *float64
			if med.Valid {
				m := med.Float64
				mp = &m
			}
			switch k {
			case "7d":
				raw.W7.Sleeps, raw.W7.Quick, raw.W7.MedianSleepS = n, q, mp
			case "30d":
				raw.W30.Sleeps, raw.W30.Quick, raw.W30.MedianSleepS = n, q, mp
			default:
				a := raw.Days[day.String]
				a.Sleeps, a.Quick = n, q
				raw.Days[day.String] = a
			}
		}
		return raw, srows.Err()
	}
}

type drainCacheEntry struct {
	resp    drainResponse
	expires time.Time
}

// drainAPI serves GET /api/vehicles/{vehicleID}/drain.
type drainAPI struct {
	store drainStore
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]drainCacheEntry
}

func newDrainAPI(store drainStore) *drainAPI {
	return &drainAPI{store: store, now: time.Now, cache: map[string]drainCacheEntry{}}
}

func (a *drainAPI) handle(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "vehicleID"))
	if id == "" {
		http.Error(w, "vehicleID required", http.StatusBadRequest)
		return
	}
	loc := time.UTC
	if tz := strings.TrimSpace(r.URL.Query().Get("tz")); tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil || strings.EqualFold(tz, "local") {
			http.Error(w, "tz must be an IANA time zone, e.g. America/Chicago", http.StatusBadRequest)
			return
		}
		loc = l
	}
	now := a.now()
	key := uid.String() + "|" + id + "|" + loc.String()
	a.mu.Lock()
	if e, ok := a.cache[key]; ok && now.Before(e.expires) {
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, e.resp)
		return
	}
	a.mu.Unlock()
	raw, err := a.store(r.Context(), uid, id, loc, now)
	if err != nil {
		http.Error(w, "drain: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resp := buildDrain(raw, now, loc)
	a.mu.Lock()
	if len(a.cache) > 512 {
		for k, e := range a.cache {
			if !now.Before(e.expires) {
				delete(a.cache, k)
			}
		}
	}
	a.cache[key] = drainCacheEntry{resp: resp, expires: now.Add(drainCacheTTL)}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}
