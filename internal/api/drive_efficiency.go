package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/apohor/rivolt/internal/db"
	"github.com/apohor/rivolt/internal/drives"
	"github.com/apohor/rivolt/internal/recap"
	"github.com/apohor/rivolt/internal/samples"
	"github.com/apohor/rivolt/internal/weather"
)

// driveEfficiencyResponse is the JSON shape returned by POST
// /api/drives/{id}/efficiency.
type driveEfficiencyResponse struct {
	Analysis       string                   `json:"analysis"`
	Factors        []recap.EfficiencyFactor `json:"factors,omitempty"`
	Recommendation string                   `json:"recommendation,omitempty"`
	Forecast       string                   `json:"forecast,omitempty"`
	Summary        string                   `json:"summary,omitempty"`
	Model          string                   `json:"model"`
	GeneratedAt    time.Time                `json:"generated_at"`
	InputTokens    int64                    `json:"input_tokens,omitempty"`
	OutputTokens   int64                    `json:"output_tokens,omitempty"`
}

// handleDriveEfficiencyPost generates an AI-driven efficiency analysis
// on demand and persists it to drive_efficiency so subsequent loads of
// the drive page hit the cache instead of re-billing the LLM key.
// Each call to this endpoint *does* re-bill (the SPA only fires it
// from an explicit Analyze / Regenerate button); the GET counterpart
// is what fetches the stored copy on page mount.
//
// The optional JSON body carries per-trip transient context (extra
// load, temperature unit) the SPA captures via the form on the
// analysis card; persisted per-vehicle settings (tire type, wheel
// size, accessories) are pulled from vehicles.metadata regardless of
// body shape. Towing is auto-detected from the persisted driveMode
// samples (Rivian 'tow' / 'towing' mode).
func handleDriveEfficiencyPost(d Deps, uid uuid.UUID) http.HandlerFunc {
	type efficiencyReq struct {
		ExtraLoadLb float64 `json:"extra_load_lb,omitempty"`
		// "f" or "c". Empty / unknown values fall back to F so a
		// legacy SPA without the field gets the historical behavior.
		// The backend can't read the SPA's preferences store
		// directly (it's localStorage, per-browser), so the SPA
		// echoes the user's pick on every request.
		TemperatureUnit string `json:"temperature_unit,omitempty"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		if d.SettingsMgr == nil {
			http.Error(w, "ai settings unavailable", http.StatusServiceUnavailable)
			return
		}
		analyzer := d.SettingsMgr.Analyzer()
		if analyzer == nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error": "no AI provider configured -- add an API key in Settings -> AI providers",
			})
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}

		drivesStore := d.Drives.For(uid)
		samplesStore := d.Samples.For(uid)
		if drivesStore == nil || samplesStore == nil {
			http.Error(w, "user stores unavailable", http.StatusServiceUnavailable)
			return
		}

		// Locate the (collapsed) drive.
		ds, err := drivesStore.ListAll(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		const (
			roundTripRadiusM = 200.0
			roundTripMaxGap  = 90 * time.Minute
		)
		ds = drives.CollapseRoundTrips(ds, roundTripRadiusM, roundTripMaxGap)
		var drv *drives.Drive
		for i := range ds {
			if ds[i].ID == driveID {
				drv = &ds[i]
				break
			}
		}
		if drv == nil {
			http.Error(w, "drive not found", http.StatusNotFound)
			return
		}

		// Sample window: trip +/- 3 min pad.
		since := drv.StartedAt.Add(-3 * time.Minute)
		end := drv.EndedAt.Add(3 * time.Minute)
		allSamples, err := samplesStore.ListSince(r.Context(), since, 100_000)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		windowed := make([]samples.Sample, 0, len(allSamples))
		for _, s := range allSamples {
			if s.At.Before(since) || s.At.After(end) {
				continue
			}
			windowed = append(windowed, s)
		}

		// Optional weather (same gate as the recap path used).
		var effWeather *recap.Weather
		if d.SettingsMgr.RecapWeatherEnabled() && drv.StartLat != 0 && drv.StartLon != 0 {
			cache := weather.NewCache(d.DB)
			snap, _ := cache.Get(r.Context(), uid, drv.ID)
			if snap == nil {
				if fetched, ferr := fetchAndCacheDriveWeather(r.Context(), cache, uid, drv); ferr == nil && fetched != nil {
					snap = fetched
				}
			}
			if snap != nil {
				effWeather = snapshotToRecapWeather(snap)
			}
		}

		// Baseline efficiency from the user's recent drives (last
		// 90 days, drives ending before this trip's start). Direct
		// computation off drives.ListAll keeps this independent of
		// the analytics package.
		baseMiPerKWh, baseDays := 0.0, 90
		{
			cutoff := drv.StartedAt.Add(-90 * 24 * time.Hour)
			var miles, energy float64
			for _, x := range ds {
				if x.ID == drv.ID {
					continue
				}
				if !x.EndedAt.Before(drv.StartedAt) {
					continue
				}
				if x.EndedAt.Before(cutoff) {
					continue
				}
				if x.EnergyUsedKWh <= 0 || x.DistanceMi <= 0 {
					continue
				}
				miles += x.DistanceMi
				energy += x.EnergyUsedKWh
			}
			if energy > 0 {
				baseMiPerKWh = miles / energy
			}
		}

		// Slightly less than the surrounding chi middleware.Timeout
		// so the LLM call observes ctx.Done first and returns a
		// real error message — chi's wrapper would otherwise win
		// the race and write 504 with no body. See the AI-bound
		// group registration above for the chi side of the budget.
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute+30*time.Second)
		defer cancel()

		// Per-trip transient context from the request body. Tolerate
		// an empty body (curl, legacy SPA).
		var req efficiencyReq
		if r.ContentLength > 0 {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}

		// Per-vehicle profile from vehicles.metadata. Best-effort:
		// if the resolve or read fails, the prompt just omits the
		// profile block — the analysis still runs.
		var profile *recap.VehicleProfile
		if drv.VehicleID != "" {
			resolver := db.NewVehicleResolver(d.DB, uid)
			if vid, err := resolver.Resolve(ctx, drv.VehicleID); err == nil {
				if dbProfile, err := db.GetVehicleProfile(ctx, d.DB, uid, vid); err == nil {
					profile = &recap.VehicleProfile{
						TireType:           dbProfile.TireType,
						WheelInches:        dbProfile.WheelInches,
						Accessories:        dbProfile.Accessories,
						DefaultExtraLoadLb: dbProfile.DefaultExtraLoadLb,
						FrequentlyTows:     dbProfile.FrequentlyTows,
						TirePlacardPSI:     dbProfile.TirePlacardPSI,
					}
				}
			}
		}

		// Default to F to preserve historical behavior when the SPA
		// doesn't send temperature_unit. Anything other than the
		// explicit "c" maps to F so a typo can't silently flip a
		// user's display.
		useF := !strings.EqualFold(req.TemperatureUnit, "c")

		res, err := recap.GenerateEfficiency(ctx, analyzer, recap.EfficiencyInputs{
			Drive:            *drv,
			Samples:          windowed,
			UseFahrenheit:    useF,
			BaselineMiPerKWh: baseMiPerKWh,
			BaselineDays:     baseDays,
			Weather:          effWeather,
			VehicleProfile:   profile,
			ExtraLoadLb:      req.ExtraLoadLb,
			Towing:           detectTowingFromSamples(windowed),
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{
				"error": err.Error(),
				"model": analyzer.ModelName(),
			})
			return
		}

		now := time.Now()
		response := driveEfficiencyResponse{
			Analysis:       res.Analysis,
			Factors:        effFactorsOf(res.Parsed),
			Recommendation: effRecommendationOf(res.Parsed),
			Forecast:       effForecastOf(res.Parsed),
			Summary:        effSummaryOf(res.Parsed),
			Model:          res.Model,
			GeneratedAt:    now,
			InputTokens:    res.InputTokens,
			OutputTokens:   res.OutputTokens,
		}

		// Persist to drive_efficiency so the next page load hits the
		// cache. Best-effort: if the upsert fails (DB hiccup, RLS
		// flip, FK violation from a deleted user) we still return
		// the freshly-computed analysis so the user sees something.
		if err := saveDriveEfficiency(r.Context(), d.DB, uid, drv.ID, response); err != nil {
			slog.WarnContext(r.Context(), "drive_efficiency: save failed",
				"err", err, "drive_id", drv.ID)
		}

		writeJSON(w, http.StatusOK, response)
	}
}

// handleDriveEfficiencyGet returns the stored efficiency analysis for
// a drive, or 404 when none has been generated yet. The SPA fetches
// this on page mount so a previously-analyzed drive shows the result
// immediately instead of an empty form. Generating a fresh analysis
// is the POST counterpart.
func handleDriveEfficiencyGet(d Deps, uid uuid.UUID) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.DB == nil {
			http.Error(w, "db unavailable", http.StatusServiceUnavailable)
			return
		}
		driveID := chi.URLParam(r, "id")
		if driveID == "" {
			http.Error(w, "missing drive id", http.StatusBadRequest)
			return
		}
		row, err := loadDriveEfficiency(r.Context(), d.DB, uid, driveID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			http.Error(w, "no analysis", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, *row)
	}
}

// saveDriveEfficiency upserts a finished analysis. The Analysis text
// is stored separately from the structured JSON so log forwarders /
// debugging tooling can read it without parsing JSONB.
func saveDriveEfficiency(
	ctx context.Context,
	db *sql.DB,
	uid uuid.UUID,
	driveID string,
	resp driveEfficiencyResponse,
) error {
	if db == nil {
		return fmt.Errorf("nil db")
	}
	body, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO drive_efficiency (
			user_id, drive_id, model, analysis_text, result_json,
			input_tokens, output_tokens, generated_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		ON CONFLICT (user_id, drive_id) DO UPDATE SET
			model         = EXCLUDED.model,
			analysis_text = EXCLUDED.analysis_text,
			result_json   = EXCLUDED.result_json,
			input_tokens  = EXCLUDED.input_tokens,
			output_tokens = EXCLUDED.output_tokens,
			generated_at  = EXCLUDED.generated_at
	`,
		uid, driveID, resp.Model, resp.Analysis, body,
		resp.InputTokens, resp.OutputTokens, resp.GeneratedAt,
	)
	return err
}

// loadDriveEfficiency returns the stored response for a drive, or nil
// when no row exists. The full driveEfficiencyResponse round-trips
// through result_json, so callers get the same JSON shape POST
// returned originally.
func loadDriveEfficiency(
	ctx context.Context,
	db *sql.DB,
	uid uuid.UUID,
	driveID string,
) (*driveEfficiencyResponse, error) {
	if db == nil {
		return nil, fmt.Errorf("nil db")
	}
	var body []byte
	err := db.QueryRowContext(ctx, `
		SELECT result_json FROM drive_efficiency
		WHERE user_id = $1 AND drive_id = $2
	`, uid, driveID).Scan(&body)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	var resp driveEfficiencyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &resp, nil
}

// detectTowingFromSamples returns true when any persisted sample
// reports Rivian's tow drive mode. Cheap O(n) scan; matches case-
// insensitively against any mode containing "tow" so we catch
// "tow", "towing", and any future Rivian-side renames.
func detectTowingFromSamples(ss []samples.Sample) bool {
	for _, s := range ss {
		if s.DriveMode == "" {
			continue
		}
		if strings.Contains(strings.ToLower(s.DriveMode), "tow") {
			return true
		}
	}
	return false
}

// Nil-safe accessors for *recap.EfficiencyParsed.
func effFactorsOf(p *recap.EfficiencyParsed) []recap.EfficiencyFactor {
	if p == nil {
		return nil
	}
	return p.Factors
}
func effRecommendationOf(p *recap.EfficiencyParsed) string {
	if p == nil {
		return ""
	}
	return p.Recommendation
}
func effForecastOf(p *recap.EfficiencyParsed) string {
	if p == nil {
		return ""
	}
	return p.Forecast
}
func effSummaryOf(p *recap.EfficiencyParsed) string {
	if p == nil {
		return ""
	}
	return p.Summary
}
