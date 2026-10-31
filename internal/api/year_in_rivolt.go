package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/charges"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/settings"
	"github.com/apohor/rivolt/internal/trips"
)

// "Your year in Rivolt": a read-only card of the signed-in user's own calendar year (UTC), computed here so the web and the
// iPhone show the same figures. Only what DA approved is in the answer: miles, kWh, charge count, the best single drive's
// efficiency (no date, no place), the longest trip (miles and month), the coldest drive (temperature and month, only when
// weather was recorded) and the cost with an estimate flag. No coordinates, places, dates finer than a month, or ids.
//
// Months and the year are UTC. Gates: at least yearMinDrives drives and yearMinCharges charges in the year, and an account at least yearMinAccountAge old.
// When a gate fails the answer is {"eligible":false,"reason":...} and carries no figures, so a card cannot be drawn by accident.

const (
	yearMinDrives     = 10
	yearMinCharges    = 3
	yearMinAccountAge = 30 * 24 * time.Hour
	// A drive shorter than this says nothing about efficiency (a parking-lot shuffle can read 9 mi/kWh).
	yearMinEfficiencyMi = 5.0
	// A "long" drive for the most-efficient-long-drive figure (ux's list).
	yearLongDriveMi = 50.0
)

type yearMonthMiles struct {
	Miles float64 `json:"miles"`
	Month string  `json:"month"` // "2026-03"
}

type yearColdest struct {
	TempF float64 `json:"temp_f"`
	Month string  `json:"month"`
}

type yearCost struct {
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	Estimated bool    `json:"estimated"` // some of the amount is an estimate (no billed or entered price)
}

type yearLongDrive struct {
	Miles    float64 `json:"miles"`
	MiPerKWh float64 `json:"mi_per_kwh"`
	Month    string  `json:"month"`
}

// yearShares are fractions 0-1 of the energy charged, by where it was charged; they sum to 1. No money.
type yearShares struct {
	Home float64 `json:"home"`
	Away float64 `json:"away"`
	Fast float64 `json:"fast"`
}

// yearResponse is GET /api/year-in-rivolt[?year=YYYY]. Ineligible answers set only Eligible, Year and Reason. Nothing in it names a
// place, a day, a time, a VIN or the truck's name.
type yearResponse struct {
	Eligible bool   `json:"eligible"`
	Year     int    `json:"year"`
	Reason   string `json:"reason,omitempty"` // "account_too_new" | "not_enough_activity"

	SinceMonth   string      `json:"since_month,omitempty"` // "2026-03": the year began for this account partway through
	DriveCount   int         `json:"drive_count,omitempty"`
	Miles        float64     `json:"miles,omitempty"`
	KWh          float64     `json:"kwh,omitempty"` // energy the drives used
	ChargeCount  int         `json:"charge_count,omitempty"`
	EnergyShares *yearShares `json:"energy_shares,omitempty"` // omitted when no charge could be placed
	// The year's miles over its kWh. Every drive's energy is the battery-percent change times the pack's capacity, so it is
	// an estimate by construction and the flag is true whenever the figure is there.
	EfficiencyMiPerKWh  float64         `json:"efficiency_mi_per_kwh,omitempty"`
	EfficiencyEstimated bool            `json:"efficiency_estimated,omitempty"`
	BestMiPerKWh        float64         `json:"best_efficiency_mi_per_kwh,omitempty"`
	LongestTrip         *yearMonthMiles `json:"longest_trip,omitempty"`
	MostEfficientLong   *yearLongDrive  `json:"most_efficient_long_drive,omitempty"`
	ColdestDrive        *yearColdest    `json:"coldest_drive,omitempty"`
	PlannedTrips        *int            `json:"planned_trips,omitempty"`
	PlannedStops        *int            `json:"planned_stops,omitempty"`
	Model               string          `json:"model,omitempty"` // only when every truck on the account is the same model
	Cost                *yearCost       `json:"cost,omitempty"`
	CostPerMile         float64         `json:"cost_per_mile,omitempty"` // in Cost.Currency; omitted with Cost
}

// yearCharge is the part of a charge the card needs.
type yearCharge struct {
	cost      float64
	estimated float64
	currency  string
	energyEst bool
	kwh       float64
	where     string // "home" | "away" | "fast" | "" (could not be placed)
}

// yearExtras are the figures that do not come from the drives or the charges.
type yearExtras struct {
	coldest      *yearColdest
	sinceMonth   string
	plannedTrips int
	plannedStops int
	model        string
}

// yearGate is "" when the card may be drawn, else why not.
func yearGate(now, createdAt time.Time, nDrives, nCharges int) string {
	switch {
	case now.Sub(createdAt) < yearMinAccountAge:
		return "account_too_new"
	case nDrives < yearMinDrives || nCharges < yearMinCharges:
		return "not_enough_activity"
	}
	return ""
}

// computeYear is the pure part: rows already limited to the year. coldest is nil when no weather was recorded.
func computeYear(year int, ds []drives.Drive, cs []yearCharge, x yearExtras) yearResponse {
	trips, stops := x.plannedTrips, x.plannedStops
	out := yearResponse{Eligible: true, Year: year, ChargeCount: len(cs), DriveCount: len(ds), ColdestDrive: x.coldest,
		SinceMonth: x.sinceMonth, PlannedTrips: &trips, PlannedStops: &stops, Model: x.model}
	var effMi, effKWh, bestLong float64
	var longest drives.Drive
	for _, d := range ds {
		out.Miles += d.DistanceMi
		out.KWh += d.EnergyUsedKWh
		if d.EnergyUsedKWh > 0 {
			effMi += d.DistanceMi
			effKWh += d.EnergyUsedKWh
		}
		if d.DistanceMi >= yearLongDriveMi && d.EnergyUsedKWh > 0 {
			if e := d.DistanceMi / d.EnergyUsedKWh; e > bestLong {
				bestLong = e
				out.MostEfficientLong = &yearLongDrive{Miles: d.DistanceMi, MiPerKWh: e, Month: d.StartedAt.UTC().Format("2006-01")}
			}
		}
		if d.DistanceMi >= yearMinEfficiencyMi && d.EnergyUsedKWh > 0 {
			if e := d.DistanceMi / d.EnergyUsedKWh; e > out.BestMiPerKWh {
				out.BestMiPerKWh = e
			}
		}
		if d.DistanceMi > longest.DistanceMi {
			longest = d
		}
	}
	if effKWh > 0 {
		out.EfficiencyMiPerKWh, out.EfficiencyEstimated = effMi/effKWh, true
	}
	var sh yearShares
	var placed float64
	for _, c := range cs {
		switch c.where {
		case "home":
			sh.Home += c.kwh
		case "away":
			sh.Away += c.kwh
		case "fast":
			sh.Fast += c.kwh
		default:
			continue
		}
		placed += c.kwh
	}
	if placed > 0 {
		sh.Home, sh.Away, sh.Fast = sh.Home/placed, sh.Away/placed, sh.Fast/placed
		out.EnergyShares = &sh
	}
	if longest.DistanceMi > 0 {
		out.LongestTrip = &yearMonthMiles{Miles: longest.DistanceMi, Month: longest.StartedAt.UTC().Format("2006-01")}
	}
	// One currency per card: the one that carries the most spend. Charges in another currency are left out of the sum.
	spend := map[string]float64{}
	for _, c := range cs {
		amt := c.cost
		if amt <= 0 {
			amt = c.estimated
		}
		spend[c.currency] += amt
	}
	best, bestAmt := "", -1.0
	for cur, amt := range spend {
		if amt > bestAmt || (amt == bestAmt && cur < best) {
			best, bestAmt = cur, amt
		}
	}
	if bestAmt > 0 {
		cost := &yearCost{Currency: best}
		for _, c := range cs {
			if c.currency != best {
				continue
			}
			if c.cost > 0 {
				cost.Amount += c.cost
				cost.Estimated = cost.Estimated || c.energyEst
			} else if c.estimated > 0 {
				cost.Amount += c.estimated
				cost.Estimated = true
			}
		}
		out.Cost = cost
		if out.Miles > 0 {
			out.CostPerMile = cost.Amount / out.Miles
		}
	}
	return out
}

func handleYearInRivolt(pool *sql.DB, uid uuid.UUID, ds *drives.Store, cs *charges.Store, settingsStore *settings.Store, ts *trips.Store, billed func(context.Context) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		now := time.Now().UTC()
		year := now.Year()
		if s := r.URL.Query().Get("year"); s != "" {
			y, err := strconv.Atoi(s)
			if err != nil || (y != now.Year() && y != now.Year()-1) {
				http.Error(w, "year must be this year or last year", http.StatusBadRequest)
				return
			}
			year = y
		}
		resp := func(v yearResponse) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
		ctx := r.Context()
		var createdAt time.Time
		if pool == nil || pool.QueryRowContext(ctx, `SELECT created_at FROM users WHERE id = $1`, uid).Scan(&createdAt) != nil {
			http.Error(w, "could not read the account", http.StatusInternalServerError)
			return
		}
		if reason := yearGate(now, createdAt, yearMinDrives, yearMinCharges); reason != "" {
			resp(yearResponse{Year: year, Reason: reason})
			return
		}
		from := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		to := from.AddDate(1, 0, 0)
		var yd []drives.Drive
		if ds != nil {
			// By date range, never by "the newest N": a heavy user's cap could stop partway through last year and the totals
			// would be short. A window too big for one answer fails closed.
			rows, err := ds.ListRange(ctx, from, to)
			if err != nil {
				http.Error(w, "could not read drives", http.StatusInternalServerError)
				return
			}
			yd = drives.FilterReparking(rows)
		}
		var yc []yearCharge
		if cs != nil {
			rows, err := cs.ListRange(ctx, from, to)
			if err != nil {
				http.Error(w, "could not read charges", http.StatusInternalServerError)
				return
			}
			// Placed as the Activity feed places them: the ledger's kind and place, the cluster of the year's own charges filling in
			// what it cannot say.
			clusters := clusterLabels(rows)
			for _, c := range decorateCharges(ctx, rows, settingsStore, billed != nil && billed(ctx)) {
				var kind, place string
				if c.Classification != nil {
					kind, place = c.Classification.Kind, c.Classification.Place
				}
				kind, place = resolvedKindPlace(kind, place, clusters[c.ID])
				where := place
				if kind == "dcfc" {
					where = "fast"
				}
				if where != "home" && where != "away" && where != "fast" {
					where = ""
				}
				cur := c.Currency
				if c.Cost <= 0 {
					cur = c.EstimatedCurrency
				}
				yc = append(yc, yearCharge{cost: c.Cost, estimated: c.EstimatedCost, currency: cur, energyEst: c.EnergyEstimated, kwh: c.EnergyAddedKWh, where: where})
			}
		}
		if reason := yearGate(now, createdAt, len(yd), len(yc)); reason != "" {
			resp(yearResponse{Year: year, Reason: reason})
			return
		}
		var coldest *yearColdest
		var tc float64
		var at time.Time
		// From the drives that exist and count: weather rows outlive their drive (no foreign key), so the join is on the drive, which
		// must be in the year, listed (a minute or more) and not a re-parking row (drives.IsReparking), and the month is the drive's.
		err := pool.QueryRowContext(ctx, `
SELECT w.temp_c, d.started_at
  FROM drives d
  JOIN drive_weather w ON w.user_id = d.user_id AND w.drive_id = d.external_id
 WHERE d.user_id = $1 AND w.temp_c IS NOT NULL
   AND d.started_at >= $2 AND d.started_at < $3
   AND d.ended_at - d.started_at >= interval '1 minute'
   AND NOT (COALESCE(d.distance_mi,0) < 0.05 AND COALESCE(d.start_soc_pct,0) = COALESCE(d.end_soc_pct,0))
 ORDER BY w.temp_c ASC, d.started_at ASC LIMIT 1`, uid, from, to).Scan(&tc, &at)
		if err == nil {
			coldest = &yearColdest{TempF: tc*1.8 + 32, Month: at.UTC().Format("2006-01")}
		} else if err != sql.ErrNoRows {
			http.Error(w, "could not read weather", http.StatusInternalServerError)
			return
		}
		x := yearExtras{coldest: coldest}
		// "Since March": the year began for this account partway through. The earlier of the account's start and its first drive
		// of the year counts, since history can be imported from before the account.
		first := createdAt.UTC()
		for _, d := range yd {
			if d.StartedAt.Before(first) {
				first = d.StartedAt
			}
		}
		if first.Year() == year && first.Month() > time.January {
			x.sinceMonth = first.Format("2006-01")
		}
		if ts != nil {
			saved, err := ts.List(ctx)
			if err != nil {
				http.Error(w, "could not read trips", http.StatusInternalServerError)
				return
			}
			for _, t := range saved {
				if t.CreatedAt.Before(from) || !t.CreatedAt.Before(to) {
					continue
				}
				x.plannedTrips++
				x.plannedStops += plannedStops(t.Plan)
			}
		}
		models, err := pool.QueryContext(ctx, `SELECT DISTINCT COALESCE(model, '') FROM vehicles WHERE user_id = $1`, uid)
		if err != nil {
			http.Error(w, "could not read vehicles", http.StatusInternalServerError)
			return
		}
		defer models.Close()
		var ms []string
		for models.Next() {
			var m string
			if err := models.Scan(&m); err != nil {
				http.Error(w, "could not read vehicles", http.StatusInternalServerError)
				return
			}
			ms = append(ms, m)
		}
		x.model = commonModel(ms)
		resp(computeYear(year, yd, yc, x))
	}
}

// plannedStops counts the charging stops in a saved trip's plan: the first route's waypoints that are neither its origin nor its
// destination. A plan that does not parse counts none.
func plannedStops(plan json.RawMessage) int {
	var p struct {
		Routes []struct {
			Waypoints []struct{ WaypointType string }
		}
	}
	if len(plan) == 0 || json.Unmarshal(plan, &p) != nil || len(p.Routes) == 0 {
		return 0
	}
	n := 0
	for _, w := range p.Routes[0].Waypoints {
		switch strings.ToUpper(w.WaypointType) {
		case "ORIGIN", "DESTINATION", "":
		default:
			n++
		}
	}
	return n
}

var yearModelRE = regexp.MustCompile(`^R[0-9][A-Z0-9]{0,3}$`)

// commonModel is the model name when every truck on the account has the same one ("R1S", "R2"), else "": the card then has no
// model in its title. An empty or odd value counts as unknown, so a card never carries free text from the vehicle record.
func commonModel(models []string) string {
	common := ""
	for _, m := range models {
		m = strings.ToUpper(strings.TrimSpace(m))
		if !yearModelRE.MatchString(m) || (common != "" && m != common) {
			return ""
		}
		common = m
	}
	return common
}
