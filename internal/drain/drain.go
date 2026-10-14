// Package drain computes the fleet's parked-drain metrics from the
// persisted vehicle_state history and exposes them to Prometheus (D1,
// the instrument for the battery-drain experiment's Gate A).
//
// Everything is derived from rows the recorder already writes; nothing
// here talks to Rivian, so computing it can't wake a car. The
// definitions match the ones already in use, so the gauges, the
// analysis queries and the Sleep chart agree:
//
//   - Parked drain (%/day): SoC lost over consecutive sample pairs
//     where the car is parked (gear P or unknown, under 1 mph, not
//     charging) at both ends, less than 24 h apart, with the odometer
//     still (< 0.2 mi) and SoC moving less than 3 points (a bigger
//     jump is an unrecorded charge or a calibration step, not drain).
//     24 × ΣΔSoC / Σhours. This is devils-advocate's D3 query.
//   - Idle-awake (h/day): hours awake while parked and not charging,
//     per 24 h of recorded power state. Intervals are capped at 20 min
//     and attributed to the state at their start, as in
//     samples.SleepActivity.
//   - Sleeps: a sleep runs from the first 'sleep' sample after a
//     non-sleep one to the next non-sleep sample. A quick re-wake is a
//     sleep that lasted under 60 s (the sleep gate's failed sleep).
//     A sleep counts in the window it ended in; one whose start is
//     before the window, or which hasn't ended, isn't counted.
package drain

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Windows the metrics are computed over.
const (
	Window24h = "24h"
	Window7d  = "7d"
)

// Minimum coverage before a rate is reported: a drain or awake rate
// over a couple of parked hours is noise. Below these the rate is
// omitted (no series) rather than reported as 0.
var minHours = map[string]float64{Window24h: 6, Window7d: 24}

// Stat is one vehicle's metrics over one window.
type Stat struct {
	VehicleID string // Rivian vehicle id, as in the logs
	Window    string // Window24h | Window7d

	// ParkedHours is the parked time the drain rate is measured over.
	ParkedHours float64
	// DrainPctPerDay is nil when ParkedHours is under the window's
	// minimum.
	DrainPctPerDay *float64
	// ObservedHours is the time with a recorded power state.
	ObservedHours float64
	// IdleAwakeHPerDay is nil when ObservedHours is under the minimum.
	IdleAwakeHPerDay *float64
	Sleeps           int
	QuickRewakes     int
	// MedianSleep is nil when no sleep ended in the window.
	MedianSleep *time.Duration
}

// fleetQuery aggregates every vehicle's rows in [$1, $3), with $2 the
// start of the 24 h window. One scan serves both windows.
const fleetQuery = `
WITH s AS (
	SELECT vehicle_id, at, battery_level_pct AS soc, odometer_mi AS odo, power_state AS ps,
	       shift_state AS gear, speed_mph AS mph, charging_state AS cs
	  FROM vehicle_state
	 WHERE at >= $1::timestamptz AND at < $3::timestamptz
), p AS (
	SELECT vehicle_id, at, soc, odo,
	       LAG(at)  OVER w AS pat,
	       LAG(soc) OVER w AS psoc,
	       LAG(odo) OVER w AS podo,
	       LAG(ps)  OVER w AS pps,
	       (COALESCE(gear, '') IN ('P', '') AND COALESCE(mph, 0) < 1
	        AND COALESCE(cs, '') NOT IN ('charging_active', 'charging_connecting')) AS idle,
	       LAG(COALESCE(gear, '') IN ('P', '') AND COALESCE(mph, 0) < 1
	           AND COALESCE(cs, '') NOT IN ('charging_active', 'charging_connecting')) OVER w AS pidle,
	       LAG(gear NOT IN ('D', 'R', 'N')
	           AND cs NOT IN ('charging_active', 'charging_connecting')) OVER w AS pparked
	  FROM s
	WINDOW w AS (PARTITION BY vehicle_id ORDER BY at)
), iv AS (
	SELECT vehicle_id, at, pps, pparked,
	       EXTRACT(EPOCH FROM at - pat) / 3600.0 AS h,
	       LEAST(EXTRACT(EPOCH FROM at - pat), 1200) / 3600.0 AS hc,
	       COALESCE(idle AND pidle AND at - pat < interval '24 hours'
	                AND soc > 0 AND psoc > 0
	                AND abs(COALESCE(odo, 0) - COALESCE(podo, 0)) < 0.2
	                AND abs(soc - psoc) < 3, false) AS drain_pair,
	       psoc - soc AS d
	  FROM p
	 WHERE pat IS NOT NULL
), agg AS (
	SELECT vehicle_id,
	       COALESCE(SUM(h)  FILTER (WHERE drain_pair), 0)                    AS park_h_7d,
	       COALESCE(SUM(d)  FILTER (WHERE drain_pair), 0)                    AS drop_7d,
	       COALESCE(SUM(h)  FILTER (WHERE drain_pair AND at >= $2::timestamptz), 0) AS park_h_1d,
	       COALESCE(SUM(d)  FILTER (WHERE drain_pair AND at >= $2::timestamptz), 0) AS drop_1d,
	       COALESCE(SUM(hc) FILTER (WHERE pps IS NOT NULL), 0)               AS obs_h_7d,
	       COALESCE(SUM(hc) FILTER (WHERE pps IS NOT NULL AND at >= $2::timestamptz), 0) AS obs_h_1d,
	       COALESCE(SUM(hc) FILTER (WHERE pps <> 'sleep' AND pparked), 0)    AS awake_h_7d,
	       COALESCE(SUM(hc) FILTER (WHERE pps <> 'sleep' AND pparked AND at >= $2::timestamptz), 0) AS awake_h_1d
	  FROM iv
	 GROUP BY vehicle_id
), ch AS (
	SELECT vehicle_id, at, ps, prev_ps,
	       LEAD(at) OVER (PARTITION BY vehicle_id ORDER BY at) AS next_at
	  FROM (SELECT vehicle_id, at, ps,
	               LAG(ps) OVER (PARTITION BY vehicle_id ORDER BY at) AS prev_ps
	          FROM s WHERE ps IS NOT NULL) x
	 WHERE ps IS DISTINCT FROM prev_ps
), sl AS (
	SELECT vehicle_id,
	       COUNT(*) AS sleeps_7d,
	       COUNT(*) FILTER (WHERE next_at - at < interval '60 seconds') AS quick_7d,
	       percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM next_at - at)) AS med_7d,
	       COUNT(*) FILTER (WHERE next_at >= $2::timestamptz) AS sleeps_1d,
	       COUNT(*) FILTER (WHERE next_at >= $2::timestamptz AND next_at - at < interval '60 seconds') AS quick_1d,
	       percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM next_at - at))
	           FILTER (WHERE next_at >= $2::timestamptz) AS med_1d
	  FROM ch
	 WHERE ps = 'sleep' AND prev_ps IS NOT NULL AND next_at IS NOT NULL
	 GROUP BY vehicle_id
)
SELECT v.rivian_vehicle_id,
       a.park_h_7d, a.drop_7d, a.park_h_1d, a.drop_1d,
       a.obs_h_7d, a.obs_h_1d, a.awake_h_7d, a.awake_h_1d,
       COALESCE(sl.sleeps_7d, 0), COALESCE(sl.quick_7d, 0), sl.med_7d,
       COALESCE(sl.sleeps_1d, 0), COALESCE(sl.quick_1d, 0), sl.med_1d
  FROM agg a
  JOIN vehicles v ON v.id = a.vehicle_id
  LEFT JOIN sl ON sl.vehicle_id = a.vehicle_id
 ORDER BY v.rivian_vehicle_id`

// Compute returns every vehicle's stats for the 24 h and 7 d windows
// ending at now, two per vehicle with any rows in the last 7 days.
// It reads across all users (the app role owns the tables).
func Compute(ctx context.Context, db *sql.DB, now time.Time) ([]Stat, error) {
	now = now.UTC()
	rows, err := db.QueryContext(ctx, fleetQuery, now.Add(-7*24*time.Hour), now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, fmt.Errorf("drain: query: %w", err)
	}
	defer rows.Close()
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.vid, &r.park7, &r.drop7, &r.park1, &r.drop1, &r.obs7, &r.obs1, &r.awake7, &r.awake1,
			&r.sleeps7, &r.quick7, &r.med7, &r.sleeps1, &r.quick1, &r.med1); err != nil {
			return nil, fmt.Errorf("drain: scan: %w", err)
		}
		rs = append(rs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("drain: rows: %w", err)
	}
	var out []Stat
	for _, r := range dedupe(rs) {
		out = append(out,
			stat(r.vid, Window24h, r.park1, r.drop1, r.obs1, r.awake1, r.sleeps1, r.quick1, r.med1),
			stat(r.vid, Window7d, r.park7, r.drop7, r.obs7, r.awake7, r.sleeps7, r.quick7, r.med7))
	}
	return out, nil
}

// row is one vehicles row's aggregates.
type row struct {
	vid                              string
	park7, drop7, park1, drop1       float64
	obs7, obs1, awake7, awake1       float64
	sleeps7, quick7, sleeps1, quick1 int
	med7, med1                       sql.NullFloat64
}

// dedupe keeps one row per Rivian vehicle id. The same car linked
// from two Rivolt accounts has two vehicles rows; exporting both would
// give two series with identical labels, which fails the whole
// /metrics scrape. The one with more recorded history wins (both
// accounts see the same car, so it's the better-covered copy).
func dedupe(rs []row) []row {
	idx := map[string]int{}
	out := rs[:0:0]
	for _, r := range rs {
		i, seen := idx[r.vid]
		switch {
		case !seen:
			idx[r.vid] = len(out)
			out = append(out, r)
		case r.obs7 > out[i].obs7:
			out[i] = r
		}
	}
	return out
}

func stat(vid, window string, parkH, drop, obsH, awakeH float64, sleeps, quick int, med sql.NullFloat64) Stat {
	s := Stat{VehicleID: vid, Window: window, ParkedHours: parkH, ObservedHours: obsH, Sleeps: sleeps, QuickRewakes: quick}
	if parkH >= minHours[window] {
		v := 24 * drop / parkH
		s.DrainPctPerDay = &v
	}
	if obsH >= minHours[window] {
		v := 24 * awakeH / obsH
		s.IdleAwakeHPerDay = &v
	}
	if med.Valid {
		d := time.Duration(med.Float64 * float64(time.Second))
		s.MedianSleep = &d
	}
	return s
}
