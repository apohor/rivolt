package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/recap"
	"github.com/apohor/rivolt/internal/weather"
)

// driveWeatherResponse mirrors the persisted drive_weather row in the
// units the SPA renders. We keep this DTO instead of leaking
// internal/weather.Snapshot directly so the SPA contract stays stable
// even if we swap the upstream provider.
type driveWeatherResponse struct {
	TempF       *float64 `json:"temp_f,omitempty"`
	ApparentF   *float64 `json:"feels_like_f,omitempty"`
	WindMPH     *float64 `json:"wind_mph,omitempty"`
	WindFromDeg *float64 `json:"wind_from_deg,omitempty"`
	// HeadwindMPH is signed: positive = headwind, negative =
	// tailwind. The SPA does its own pretty-print.
	HeadwindMPH *float64 `json:"headwind_mph,omitempty"`
	PrecipIn    *float64 `json:"precip_in,omitempty"`
	HumidityPct *float64 `json:"humidity_pct,omitempty"`
	Conditions  string   `json:"conditions,omitempty"`
}

// driveWeatherBackfillResponse is the JSON the SPA polls. Counts are
// cumulative for the single call; remaining is recomputed at the end
// so the client can stop polling when it hits zero.
type driveWeatherBackfillResponse struct {
	Disabled  bool `json:"disabled"`
	Processed int  `json:"processed"`
	Succeeded int  `json:"succeeded"`
	Failed    int  `json:"failed"`
	Remaining int  `json:"remaining"`
}

// handleDriveWeatherBackfill enriches historical drives with weather
// snapshots. Each call processes at most weatherBackfillBatch drives
// that don't yet have a cache row, so a slow upstream can't lock up
// a worker; the SPA polls until remaining == 0.
//
// Gated on RecapWeatherEnabled — backfill is the same data egress
// the per-recap fetch performs, just amortised across the archive.
// If the pref is off we return 200 with disabled=true so the UI can
// short-circuit instead of guessing from a 4xx.
func handleDriveWeatherBackfill(d Deps, uid uuid.UUID) http.HandlerFunc {
	// Bounded so one click can't spin a worker for minutes. Open-Meteo
	// is fast (~150ms) but we still want a hard ceiling per request.
	const weatherBackfillBatch = 25
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if d.SettingsMgr == nil {
			http.Error(w, "settings unavailable", http.StatusServiceUnavailable)
			return
		}
		if !d.SettingsMgr.RecapWeatherEnabled() {
			writeJSON(w, http.StatusOK, driveWeatherBackfillResponse{Disabled: true})
			return
		}
		drivesStore := d.Drives.For(uid)
		if drivesStore == nil {
			http.Error(w, "user stores unavailable", http.StatusServiceUnavailable)
			return
		}
		ds, err := drivesStore.ListAll(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Walk newest-first so a user kicking off backfill on a fresh
		// install sees their recent drives enriched first; ListAll is
		// already sorted by start time descending.
		cache := weather.NewCache(d.DB)
		resp := driveWeatherBackfillResponse{}
		for i := range ds {
			drv := &ds[i]
			// Skip drives without a usable start fix — we can't ask
			// Open-Meteo "what was the weather at (0,0)" usefully,
			// and these rows would otherwise stay "remaining" forever.
			if drv.StartLat == 0 && drv.StartLon == 0 {
				continue
			}
			// "Done" means the time-series rows are populated.
			// Checking the snapshot alone would skip drives that
			// were enriched before the series feature shipped, so
			// users who already ran backfill once would never get
			// their graphs filled in.
			if existing, _ := cache.GetSeries(r.Context(), uid, drv.ID); len(existing) > 0 {
				continue
			}
			if resp.Processed >= weatherBackfillBatch {
				resp.Remaining++
				continue
			}
			resp.Processed++
			if _, err := fetchAndCacheDriveWeather(r.Context(), cache, uid, drv); err != nil {
				resp.Failed++
				if d.Logger != nil {
					d.Logger.Warn("weather backfill fetch failed", "err", err.Error(), "drive_id", drv.ID)
				}
				continue
			}
			resp.Succeeded++
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// fetchAndCacheDriveWeather populates both the start-hour snapshot
// (drive_weather, used by the recap prompt and the start-strip)
// and the per-cadence time series (drive_weather_series, used by
// the drive-detail weather panel). Returns the snapshot so the
// recap path can render it inline; returns (nil, nil) when the
// drive has no usable start fix.
//
// Each upstream call is given its own bounded timeout so a slow
// provider can't lock up the request. A series fetch failure
// after a successful snapshot fetch is logged at the call site
// but does not roll back the snapshot -- the recap can still
// render with start-hour data while the chart stays empty.
func fetchAndCacheDriveWeather(ctx context.Context, cache *weather.Cache, uid uuid.UUID, drv *drives.Drive) (*weather.Snapshot, error) {
	if drv == nil {
		return nil, nil
	}
	return weather.FetchAndCache(
		ctx, cache, uid, drv.ID,
		drv.StartedAt, drv.EndedAt,
		drv.StartLat, drv.StartLon,
		drv.EndLat, drv.EndLon,
	)
}

// handleDriveWeatherGet returns the persisted weather snapshot for
// (uid, driveID), or 404 when no row exists. Lightweight read off
// the drive_weather cache; never calls Open-Meteo. Independent of
// the recap path so the detail-page chart can render even when no
// AI recap was generated for this drive.
func handleDriveWeatherGet(pool *sql.DB, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}
		snap, err := loadDriveWeather(r.Context(), pool, uid, driveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if snap == nil {
			http.Error(w, "no weather cached for this drive", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, snap)
	}
}

// driveWeatherSamplePoint is one entry in the time-series response.
// Matches the SPA-facing units of driveWeatherResponse so the chart
// renderer doesn't carry conversion logic.
type driveWeatherSamplePoint struct {
	At             time.Time `json:"at"`
	CadenceMinutes int       `json:"cadence_minutes"`
	TempF          *float64  `json:"temp_f,omitempty"`
	ApparentF      *float64  `json:"feels_like_f,omitempty"`
	WindMPH        *float64  `json:"wind_mph,omitempty"`
	WindFromDeg    *float64  `json:"wind_from_deg,omitempty"`
	HeadwindMPH    *float64  `json:"headwind_mph,omitempty"`
	PrecipIn       *float64  `json:"precip_in,omitempty"`
	HumidityPct    *float64  `json:"humidity_pct,omitempty"`
	Conditions     string    `json:"conditions,omitempty"`
}

type driveWeatherSeriesResponse struct {
	Points []driveWeatherSamplePoint `json:"points"`
}

// handleDriveWeatherSeriesGet returns the cached time series for
// (uid, driveID). Returns 200 with an empty `points` array (not 404)
// when no rows exist so the SPA can render a "no chart data" affordance
// instead of treating the missing series as an error -- the start-hour
// snapshot endpoint already handles the not-found case.
func handleDriveWeatherSeriesGet(pool *sql.DB, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if pool == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}
		cache := weather.NewCache(pool)
		rows, err := cache.GetSeries(r.Context(), uid, driveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := driveWeatherSeriesResponse{Points: make([]driveWeatherSamplePoint, 0, len(rows))}
		for _, row := range rows {
			p := driveWeatherSamplePoint{At: row.SampledAt, CadenceMinutes: row.CadenceMinutes}
			if row.HasTemp {
				v := row.TempC*1.8 + 32
				p.TempF = &v
			}
			if row.HasApparent {
				v := row.ApparentTempC*1.8 + 32
				p.ApparentF = &v
			}
			if row.HasWind {
				v := row.WindKPH * 0.621371
				p.WindMPH = &v
				wd := row.WindDirDeg
				p.WindFromDeg = &wd
			}
			if row.HasHeadwind {
				v := row.HeadwindKPH * 0.621371
				p.HeadwindMPH = &v
			}
			if row.HasPrecip {
				v := row.PrecipMM * 0.0393701
				p.PrecipIn = &v
			}
			if row.HasHumidity {
				v := row.HumidityPct
				p.HumidityPct = &v
			}
			if row.HasConditions {
				p.Conditions = row.Conditions
			}
			out.Points = append(out.Points, p)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// loadDriveWeather returns the persisted weather snapshot for
// (uid, driveID) in the SPA's units (F, mph, in). Returns (nil, nil)
// when no row exists.
func loadDriveWeather(ctx context.Context, pool *sql.DB, uid uuid.UUID, driveID string) (*driveWeatherResponse, error) {
	if pool == nil {
		return nil, nil
	}
	var (
		tC, atC, wKPH, wDir, hwKPH, pMM, hPct sql.NullFloat64
		cond                                  sql.NullString
	)
	err := pool.QueryRowContext(ctx, `
SELECT temp_c, apparent_temp_c, wind_kph, wind_dir_deg, headwind_kph,
       precip_mm, humidity_pct, conditions
FROM drive_weather
WHERE user_id = $1 AND drive_id = $2
`, uid, driveID).Scan(&tC, &atC, &wKPH, &wDir, &hwKPH, &pMM, &hPct, &cond)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &driveWeatherResponse{}
	if tC.Valid {
		v := tC.Float64*1.8 + 32
		out.TempF = &v
	}
	if atC.Valid {
		v := atC.Float64*1.8 + 32
		out.ApparentF = &v
	}
	if wKPH.Valid {
		v := wKPH.Float64 * 0.621371
		out.WindMPH = &v
	}
	if wDir.Valid {
		v := wDir.Float64
		out.WindFromDeg = &v
	}
	if hwKPH.Valid {
		v := hwKPH.Float64 * 0.621371
		out.HeadwindMPH = &v
	}
	if pMM.Valid {
		v := pMM.Float64 * 0.0393701
		out.PrecipIn = &v
	}
	if hPct.Valid {
		v := hPct.Float64
		out.HumidityPct = &v
	}
	if cond.Valid {
		out.Conditions = cond.String
	}
	return out, nil
}

// snapshotToResponseWeather converts the metric-base snapshot to the
// imperial DTO the SPA expects. The cache always stores metric so the
// conversion lives at the API boundary.
func snapshotToResponseWeather(s *weather.Snapshot) *driveWeatherResponse {
	if s == nil {
		return nil
	}
	out := &driveWeatherResponse{Conditions: s.Conditions}
	if s.HasTemp {
		v := s.TempC*1.8 + 32
		out.TempF = &v
	}
	if s.HasApparent {
		v := s.ApparentTempC*1.8 + 32
		out.ApparentF = &v
	}
	if s.HasWind {
		v := s.WindKPH * 0.621371
		out.WindMPH = &v
		dir := s.WindDirDeg
		out.WindFromDeg = &dir
	}
	if s.HasHeadwind {
		v := s.HeadwindKPH * 0.621371
		out.HeadwindMPH = &v
	}
	if s.HasPrecip {
		v := s.PrecipMM * 0.0393701
		out.PrecipIn = &v
	}
	if s.HasHumidity {
		v := s.HumidityPct
		out.HumidityPct = &v
	}
	return out
}

// snapshotToRecapWeather lifts an internal/weather.Snapshot into the
// recap.Weather DTO the prompt builder consumes. Both shapes have the
// same field names; the conversion is mechanical.
func snapshotToRecapWeather(s *weather.Snapshot) *recap.Weather {
	if s == nil {
		return nil
	}
	return &recap.Weather{
		TempC:         s.TempC,
		ApparentTempC: s.ApparentTempC,
		WindKPH:       s.WindKPH,
		WindDirDeg:    s.WindDirDeg,
		HeadwindKPH:   s.HeadwindKPH,
		PrecipMM:      s.PrecipMM,
		HumidityPct:   s.HumidityPct,
		Conditions:    s.Conditions,
		HasTemp:       s.HasTemp,
		HasApparent:   s.HasApparent,
		HasWind:       s.HasWind,
		HasHeadwind:   s.HasHeadwind,
		HasPrecip:     s.HasPrecip,
		HasHumidity:   s.HasHumidity,
		HasConditions: s.HasConditions,
	}
}
