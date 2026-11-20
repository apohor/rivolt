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

// Parked-series endpoint (design now_parked_graph): the last 24 h of one truck's battery while it was parked, with its sleeps and
// wake-ups, for the Now card's "Battery while parked" graph. Stored vehicle_state rows only (never Rivian, so it cannot wake the truck),
// a plain DB read on any pod. Every figure the card shows is computed here, so the web and the iPhone app print the same numbers.
//
//   - a point is a parked row (gear P or none, under 1 mph, not charging) with a battery level, at most one per 10 minutes plus every
//     row where the state changes; seg numbers the runs of points, a drive or a charge between two points starts a new one;
//   - a sleep is a run of power_state 'sleep' among the parked rows, ending at the next row of any kind (open while it is the newest);
//   - a wake-up is a sleep followed by an awake parked row; its cost is the battery lost from the end of the sleep to the next sleep
//     (or the newest row), never negative;
//   - the rate is the current parked run's loss per day, stated only after it has lasted parkedSeriesMinRateH with enough points.
//
// Sparse data is normal: with fewer than parkedSeriesMinPoints points over parkedSeriesMinSpan nothing is returned.
const (
	parkedSeriesWindow    = 24 * time.Hour
	parkedSeriesMinPoints = 4
	parkedSeriesMinSpan   = time.Hour
	parkedSeriesMinRateH  = 3.0
	// The projection targets the next 08:00 local at least this far ahead.
	parkedSeriesProjectMinAhead = 12 * time.Hour
	parkedSeriesCacheTTL        = time.Minute
)

// parkedRow is one down-sampled vehicle_state row. Kind is 'p' parked, 'd' driving, 'c' charging.
type parkedRow struct {
	At      time.Time
	SoC     *float64
	RangeMi *float64
	Sleep   bool
	Kind    string
}

type parkedPoint struct {
	T   time.Time `json:"t"`
	SoC float64   `json:"soc"`
	Seg int       `json:"seg"`
}

type parkedSleep struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Open is true while the truck is still asleep (To is then the time of the answer).
	Open bool `json:"open"`
}

type parkedWake struct {
	T         time.Time `json:"t"`
	SoCBefore float64   `json:"soc_before"`
	SoCAfter  float64   `json:"soc_after"`
}

type parkedProjection struct {
	At  time.Time `json:"at"`
	SoC float64   `json:"soc"`
}

type parkedFigures struct {
	AsleepTodayS int64 `json:"asleep_today_s"`
	WakeUps      int   `json:"wake_ups"`
	// WakeCostPct is the average battery lost per wake-up; null with no wake-up.
	WakeCostPct *float64 `json:"wake_cost_pct"`
	// IdlePctPerDay and IdleMiPerDay: the parked loss per day and its range in miles; null until parked long enough or when it gained.
	IdlePctPerDay *float64 `json:"idle_pct_per_day"`
	IdleMiPerDay  *float64 `json:"idle_mi_per_day"`
}

type parkedSeriesResponse struct {
	Version int       `json:"version"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	// ParkedSince is the start of the current parked run; null when it began before the window or the truck is not parked.
	ParkedSince *time.Time `json:"parked_since"`
	Parked      bool       `json:"parked"`
	// ChangePct is the battery change over the current parked run (negative is a loss); null with fewer than two points in it.
	ChangePct  *float64          `json:"change_pct"`
	Points     []parkedPoint     `json:"points"`
	Sleeps     []parkedSleep     `json:"sleeps"`
	Wakes      []parkedWake      `json:"wakes"`
	Figures    *parkedFigures    `json:"figures"`
	Projection *parkedProjection `json:"projection"`
}

// buildParkedSeries turns the rows (oldest first) into the response. loc is the user's time zone (local midnight for "asleep today",
// 08:00 for the projection).
func buildParkedSeries(rows []parkedRow, now time.Time, loc *time.Location) parkedSeriesResponse {
	now = now.UTC()
	out := parkedSeriesResponse{Version: 1, From: now.Add(-parkedSeriesWindow), To: now,
		Points: []parkedPoint{}, Sleeps: []parkedSleep{}, Wakes: []parkedWake{}}

	// Points and the segments they sit in.
	seg := 0
	for _, r := range rows {
		if r.Kind != "p" {
			seg++
			continue
		}
		if r.SoC == nil {
			continue
		}
		out.Points = append(out.Points, parkedPoint{T: r.At.UTC(), SoC: *r.SoC, Seg: seg})
	}
	if len(out.Points) < parkedSeriesMinPoints || out.Points[len(out.Points)-1].T.Sub(out.Points[0].T) < parkedSeriesMinSpan {
		out.Points = []parkedPoint{}
		return out
	}

	// Sleeps and wake-ups over the parked rows.
	type run struct {
		from, to   time.Time
		soc0, soc1 *float64
	}
	var sleeps []run
	var open *run
	var wakes []parkedWake
	var awakeFrom *parkedWake // the wake-up whose cost is still being measured
	var lastSoC *float64
	cost := []float64{}
	closeAwake := func(soc *float64) {
		if awakeFrom != nil && soc != nil {
			cost = append(cost, math.Max(0, awakeFrom.SoCAfter-*soc))
		}
		awakeFrom = nil
	}
	for _, r := range rows {
		if r.Kind != "p" {
			if open != nil {
				open.to = r.At.UTC()
				sleeps = append(sleeps, *open)
				open = nil
			}
			closeAwake(lastSoC)
			continue
		}
		if r.Sleep {
			closeAwake(r.SoC)
			if open == nil {
				open = &run{from: r.At.UTC(), soc0: r.SoC}
			}
			open.soc1 = r.SoC
			lastSoC = r.SoC
			continue
		}
		if open != nil { // a sleep ends at this awake parked row: a wake-up
			open.to = r.At.UTC()
			sleeps = append(sleeps, *open)
			if open.soc1 != nil && r.SoC != nil {
				w := parkedWake{T: r.At.UTC(), SoCBefore: *open.soc1, SoCAfter: *r.SoC}
				wakes = append(wakes, w)
				awakeFrom = &wakes[len(wakes)-1]
			}
			open = nil
		}
		if r.SoC != nil {
			lastSoC = r.SoC
		}
	}
	openSleep := open != nil
	if open != nil {
		open.to = now
		sleeps = append(sleeps, *open)
	} else {
		closeAwake(lastSoC)
	}
	midnight := localMidnight(loc, now)
	var asleepToday int64
	for _, s := range sleeps {
		if s.to.Before(out.From) {
			continue
		}
		from := s.from
		if from.Before(out.From) {
			from = out.From
		}
		out.Sleeps = append(out.Sleeps, parkedSleep{From: from, To: s.to, Open: openSleep && s.to.Equal(now)})
		a, b := s.from, s.to
		if a.Before(midnight) {
			a = midnight
		}
		if b.After(a) {
			asleepToday += int64(b.Sub(a) / time.Second)
		}
	}
	for _, w := range wakes {
		if !w.T.Before(out.From) {
			out.Wakes = append(out.Wakes, w)
		}
	}

	// The current parked run: the trailing parked rows.
	last := out.Points[len(out.Points)-1]
	out.Parked = rows[len(rows)-1].Kind == "p"
	var cur []parkedPoint
	if out.Parked {
		for i := len(out.Points) - 1; i >= 0 && out.Points[i].Seg == last.Seg; i-- {
			cur = append([]parkedPoint{out.Points[i]}, cur...)
		}
		if last.Seg > 0 { // a drive or a charge came before it inside the window
			t := cur[0].T
			out.ParkedSince = &t
		}
		if len(cur) >= 2 {
			d := cur[len(cur)-1].SoC - cur[0].SoC
			out.ChangePct = &d
		}
	}

	f := &parkedFigures{AsleepTodayS: asleepToday, WakeUps: len(out.Wakes)}
	if len(cost) > 0 {
		var sum float64
		for _, c := range cost {
			sum += c
		}
		avg := sum / float64(len(cost))
		f.WakeCostPct = &avg
	}
	if out.Parked && len(cur) >= parkedSeriesMinPoints {
		hours := cur[len(cur)-1].T.Sub(cur[0].T).Hours()
		loss := cur[0].SoC - cur[len(cur)-1].SoC
		if hours >= parkedSeriesMinRateH && loss > 0 {
			rate := loss / hours * 24
			f.IdlePctPerDay = &rate
			if rm, soc := newestRange(rows); rm != nil && soc != nil && *soc > 0 {
				mi := rate * *rm / *soc
				f.IdleMiPerDay = &mi
			}
			at := nextLocalHour(loc, now.Add(parkedSeriesProjectMinAhead), 8)
			p := last.SoC - rate*at.Sub(last.T).Hours()/24
			if p < 0 {
				p = 0
			}
			out.Projection = &parkedProjection{At: at.UTC(), SoC: p}
		}
	}
	out.Figures = f
	return out
}

// newestRange is the newest row's range and battery level, for range per percent.
func newestRange(rows []parkedRow) (*float64, *float64) {
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].RangeMi != nil && rows[i].SoC != nil {
			return rows[i].RangeMi, rows[i].SoC
		}
	}
	return nil, nil
}

func localMidnight(loc *time.Location, t time.Time) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// nextLocalHour is the first moment at or after t that is hour:00 local.
func nextLocalHour(loc *time.Location, t time.Time, hour int) time.Time {
	y, m, d := t.In(loc).Date()
	c := time.Date(y, m, d, hour, 0, 0, 0, loc)
	if c.Before(t) {
		c = time.Date(y, m, d+1, hour, 0, 0, 0, loc)
	}
	return c
}

// parkedSeriesSQL: $1 user, $2 Rivian vehicle id, $3 window start, $4 now. Uses the (vehicle_id, at) primary key. One row per
// 10-minute bucket (its newest) plus every row where the kind or the power state changes; the window functions run over the bucketed
// rows of one vehicle for 24 h only.
const parkedSeriesSQL = `
WITH v AS (
	SELECT id FROM vehicles WHERE user_id = $1 AND rivian_vehicle_id = $2
), s AS (
	SELECT at, battery_level_pct AS soc, range_mi, (power_state = 'sleep') AS sleep,
	       CASE WHEN COALESCE(charging_state,'') IN ('charging_active','charging_connecting') THEN 'c'
	            WHEN COALESCE(shift_state,'') IN ('D','R','N') OR COALESCE(speed_mph,0) >= 1 THEN 'd'
	            ELSE 'p' END AS kind
	  FROM vehicle_state
	 WHERE user_id = $1 AND vehicle_id = (SELECT id FROM v) AND at >= $3::timestamptz AND at <= $4::timestamptz
), w AS (
	SELECT *, LAG(kind) OVER o AS pk, LAG(sleep) OVER o AS ps,
	       ROW_NUMBER() OVER (PARTITION BY floor(extract(epoch FROM at) / 600) ORDER BY at DESC) AS rn
	  FROM s WINDOW o AS (ORDER BY at)
)
SELECT at, soc, range_mi, COALESCE(sleep, false), kind FROM w
 WHERE rn = 1 OR pk IS DISTINCT FROM kind OR ps IS DISTINCT FROM sleep
 ORDER BY at`

type parkedSeriesStore func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) ([]parkedRow, error)

func newParkedSeriesStore(pool *sql.DB) parkedSeriesStore {
	return func(ctx context.Context, uid uuid.UUID, rivianID string, now time.Time) ([]parkedRow, error) {
		rows, err := pool.QueryContext(ctx, parkedSeriesSQL, uid, rivianID, now.Add(-parkedSeriesWindow).UTC(), now.UTC())
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []parkedRow
		for rows.Next() {
			var r parkedRow
			var soc, rng sql.NullFloat64
			if err := rows.Scan(&r.At, &soc, &rng, &r.Sleep, &r.Kind); err != nil {
				return nil, err
			}
			if soc.Valid {
				r.SoC = &soc.Float64
			}
			if rng.Valid {
				r.RangeMi = &rng.Float64
			}
			out = append(out, r)
		}
		return out, rows.Err()
	}
}

type parkedCacheEntry struct {
	resp    parkedSeriesResponse
	expires time.Time
}

type parkedSeriesAPI struct {
	store parkedSeriesStore
	now   func() time.Time

	mu    sync.Mutex
	cache map[string]parkedCacheEntry
}

func newParkedSeriesAPI(store parkedSeriesStore) *parkedSeriesAPI {
	return &parkedSeriesAPI{store: store, now: time.Now, cache: map[string]parkedCacheEntry{}}
}

func (a *parkedSeriesAPI) handle(uid uuid.UUID, w http.ResponseWriter, r *http.Request) {
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
	rows, err := a.store(r.Context(), uid, id, now)
	if err != nil {
		http.Error(w, "parked series: "+err.Error(), http.StatusInternalServerError)
		return
	}
	resp := buildParkedSeries(rows, now, loc)
	a.mu.Lock()
	if len(a.cache) > 512 {
		for k, e := range a.cache {
			if !now.Before(e.expires) {
				delete(a.cache, k)
			}
		}
	}
	a.cache[key] = parkedCacheEntry{resp: resp, expires: now.Add(parkedSeriesCacheTTL)}
	a.mu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}
