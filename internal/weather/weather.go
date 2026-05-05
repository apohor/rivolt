// Package weather enriches a drive with the conditions at its start
// point and time. Used by the trip recap so the model can attribute
// efficiency swings to weather instead of inventing the comparison.
//
// # Privacy posture
//
// Recap.go's header pins the rule: no GPS coordinates leave the box.
// That's strictly broken by hitting any external weather API, so we
// mitigate three ways:
//
//  1. Coords are rounded to CoarseRound (0.1 deg, ~11 km) before the
//     upstream request. Open-Meteo's grid is ~9 km, so the rounding
//     costs nothing in accuracy.
//  2. The whole feature is gated behind an opt-in operator setting
//     (recap.weather_enabled). Default off.
//  3. Results are cached per drive in drive_weather and never
//     re-fetched on regenerate.
//
// # Source
//
// Open-Meteo (https://open-meteo.com/) free archive endpoint. No API
// key required, no quota issues at homelab volume. ERA5 reanalysis
// ~9 km grid, hourly back to 1940. We pull a single hour (the trip
// start hour, UTC truncated) and lift the closest reading.
package weather

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CoarseRound is the lat/lon rounding step in degrees applied before
// the upstream request. 0.1 ~= 11 km.
const CoarseRound = 0.1

// Provider is the upstream identifier we stamp into the cache row so
// future swaps stay clean.
const Provider = "open-meteo"

// Snapshot is the prompt-ready view of weather at a drive's start.
// Fields are zero when the upstream omitted the metric, with one
// exception: WindDirDeg is documented as "from direction" by Open-
// Meteo (degrees clockwise from north), so 0.0 is a valid value;
// callers must check HasWind to disambiguate.
type Snapshot struct {
	TempC         float64
	ApparentTempC float64
	WindKPH       float64
	WindDirDeg    float64 // direction the wind is coming FROM (Open-Meteo convention)
	HeadwindKPH   float64 // signed projection onto trip bearing; negative = tailwind
	PrecipMM      float64
	HumidityPct   float64
	Conditions    string // short label from WMO weather code
	HasTemp       bool
	HasWind       bool
	HasPrecip     bool
	HasHumidity   bool
	HasConditions bool
	HasApparent   bool
	HasHeadwind   bool
}

// Coarsen rounds (lat, lon) to CoarseRound before disclosure.
func Coarsen(lat, lon float64) (float64, float64) {
	r := func(v float64) float64 {
		return math.Round(v/CoarseRound) * CoarseRound
	}
	// math.Round can produce -0; normalize to +0 so cache keys are
	// stable.
	return r(lat) + 0, r(lon) + 0
}

// Bearing returns the initial compass bearing in degrees (0-360)
// from (lat1,lon1) to (lat2,lon2). Used to project wind onto the
// trip direction.
func Bearing(lat1, lon1, lat2, lon2 float64) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }
	deg := func(r float64) float64 { return r * 180 / math.Pi }
	dLon := rad(lon2 - lon1)
	y := math.Sin(dLon) * math.Cos(rad(lat2))
	x := math.Cos(rad(lat1))*math.Sin(rad(lat2)) -
		math.Sin(rad(lat1))*math.Cos(rad(lat2))*math.Cos(dLon)
	b := math.Mod(deg(math.Atan2(y, x))+360, 360)
	return b
}

// Headwind returns the signed component of the wind vector along the
// trip bearing in kph. Open-Meteo reports wind direction as the
// direction the wind is coming FROM, so a wind from due-west (270)
// against a bearing of due-east (90) is a 180 deg disagreement, i.e.
// pure headwind.
//
// Result interpretation:
//
//	+v  -> headwind of v kph (slows you down)
//	-v  -> tailwind of v kph (speeds you up)
func Headwind(windKPH, windFromDeg, tripBearingDeg float64) float64 {
	rel := windFromDeg - tripBearingDeg
	return windKPH * math.Cos(rel*math.Pi/180)
}

// Cache is the persistent cache backing for drive weather. Methods
// are safe to call with a nil Cache; they degrade to "no-op". The
// recap handler keeps a single Cache for the lifetime of the
// process.
type Cache struct {
	db *sql.DB
}

// NewCache wraps a pool. Returns nil on a nil pool so callers can
// stay branchless.
func NewCache(db *sql.DB) *Cache {
	if db == nil {
		return nil
	}
	return &Cache{db: db}
}

// Get returns the cached snapshot for (uid, driveID), or (nil, nil)
// when there's no row.
func (c *Cache) Get(ctx context.Context, uid uuid.UUID, driveID string) (*Snapshot, error) {
	if c == nil {
		return nil, nil
	}
	var (
		t, at, w, wd, hw, p, h sql.NullFloat64
		cond                   sql.NullString
	)
	err := c.db.QueryRowContext(ctx, `
SELECT temp_c, apparent_temp_c, wind_kph, wind_dir_deg, headwind_kph,
       precip_mm, humidity_pct, conditions
FROM drive_weather
WHERE user_id = $1 AND drive_id = $2
`, uid, driveID).Scan(&t, &at, &w, &wd, &hw, &p, &h, &cond)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s := &Snapshot{}
	if t.Valid {
		s.TempC = t.Float64
		s.HasTemp = true
	}
	if at.Valid {
		s.ApparentTempC = at.Float64
		s.HasApparent = true
	}
	if w.Valid {
		s.WindKPH = w.Float64
		s.HasWind = true
	}
	if wd.Valid {
		s.WindDirDeg = wd.Float64
	}
	if hw.Valid {
		s.HeadwindKPH = hw.Float64
		s.HasHeadwind = true
	}
	if p.Valid {
		s.PrecipMM = p.Float64
		s.HasPrecip = true
	}
	if h.Valid {
		s.HumidityPct = h.Float64
		s.HasHumidity = true
	}
	if cond.Valid {
		s.Conditions = cond.String
		s.HasConditions = cond.String != ""
	}
	return s, nil
}

// Put writes a snapshot. Uses ON CONFLICT so a regenerate after a
// successful first fetch is a no-op rather than churning the row.
func (c *Cache) Put(ctx context.Context, uid uuid.UUID, driveID string, lat, lon float64, sampledAt time.Time, s *Snapshot) error {
	if c == nil || s == nil {
		return nil
	}
	nf := func(ok bool, v float64) any {
		if !ok {
			return nil
		}
		return v
	}
	ns := func(ok bool, v string) any {
		if !ok {
			return nil
		}
		return v
	}
	_, err := c.db.ExecContext(ctx, `
INSERT INTO drive_weather (
    user_id, drive_id, coarse_lat, coarse_lon, sampled_at, provider,
    temp_c, apparent_temp_c, wind_kph, wind_dir_deg, headwind_kph,
    precip_mm, humidity_pct, conditions
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
ON CONFLICT (user_id, drive_id) DO UPDATE SET
    coarse_lat = EXCLUDED.coarse_lat,
    coarse_lon = EXCLUDED.coarse_lon,
    sampled_at = EXCLUDED.sampled_at,
    provider = EXCLUDED.provider,
    temp_c = EXCLUDED.temp_c,
    apparent_temp_c = EXCLUDED.apparent_temp_c,
    wind_kph = EXCLUDED.wind_kph,
    wind_dir_deg = EXCLUDED.wind_dir_deg,
    headwind_kph = EXCLUDED.headwind_kph,
    precip_mm = EXCLUDED.precip_mm,
    humidity_pct = EXCLUDED.humidity_pct,
    conditions = EXCLUDED.conditions,
    cached_at = NOW()
`, uid, driveID, lat, lon, sampledAt.UTC(), Provider,
		nf(s.HasTemp, s.TempC),
		nf(s.HasApparent, s.ApparentTempC),
		nf(s.HasWind, s.WindKPH),
		nf(s.HasWind, s.WindDirDeg),
		nf(s.HasHeadwind, s.HeadwindKPH),
		nf(s.HasPrecip, s.PrecipMM),
		nf(s.HasHumidity, s.HumidityPct),
		ns(s.HasConditions, s.Conditions),
	)
	return err
}

// SeriesRow is one sample of weather along a drive. Used to back
// the temperature + precipitation graph on the drive detail page.
// Same metric base units as Snapshot. CadenceMinutes is 15 (forecast
// API minutely_15) or 60 (archive API hourly), set by the fetcher.
type SeriesRow struct {
	SampledAt      time.Time
	CadenceMinutes int
	TempC          float64
	ApparentTempC  float64
	WindKPH        float64
	WindDirDeg     float64
	HeadwindKPH    float64
	PrecipMM       float64
	HumidityPct    float64
	Conditions     string
	HasTemp        bool
	HasApparent    bool
	HasWind        bool
	HasHeadwind    bool
	HasPrecip      bool
	HasHumidity    bool
	HasConditions  bool
}

// GetSeries returns the cached time-series rows for (uid, driveID),
// ordered by sampled_at. (nil, nil) when no rows exist.
func (c *Cache) GetSeries(ctx context.Context, uid uuid.UUID, driveID string) ([]SeriesRow, error) {
	if c == nil {
		return nil, nil
	}
	rows, err := c.db.QueryContext(ctx, `
SELECT sampled_at, cadence_minutes, temp_c, apparent_temp_c, wind_kph, wind_dir_deg, headwind_kph,
       precip_mm, humidity_pct, conditions
FROM drive_weather_series
WHERE user_id = $1 AND drive_id = $2
ORDER BY sampled_at ASC
`, uid, driveID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesRow
	for rows.Next() {
		var (
			sa                       time.Time
			cad                      int16
			t, at_, w, wd, hw, p, hu sql.NullFloat64
			cond                     sql.NullString
		)
		if err := rows.Scan(&sa, &cad, &t, &at_, &w, &wd, &hw, &p, &hu, &cond); err != nil {
			return nil, err
		}
		row := SeriesRow{SampledAt: sa, CadenceMinutes: int(cad)}
		if t.Valid {
			row.TempC = t.Float64
			row.HasTemp = true
		}
		if at_.Valid {
			row.ApparentTempC = at_.Float64
			row.HasApparent = true
		}
		if w.Valid {
			row.WindKPH = w.Float64
			row.HasWind = true
		}
		if wd.Valid {
			row.WindDirDeg = wd.Float64
		}
		if hw.Valid {
			row.HeadwindKPH = hw.Float64
			row.HasHeadwind = true
		}
		if p.Valid {
			row.PrecipMM = p.Float64
			row.HasPrecip = true
		}
		if hu.Valid {
			row.HumidityPct = hu.Float64
			row.HasHumidity = true
		}
		if cond.Valid {
			row.Conditions = cond.String
			row.HasConditions = cond.String != ""
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// PutSeries persists rows for (uid, driveID). Replaces any existing
// rows for that drive in a single transaction so a partial re-fetch
// can never leave a torn series.
func (c *Cache) PutSeries(ctx context.Context, uid uuid.UUID, driveID string, lat, lon float64, rows []SeriesRow) error {
	if c == nil || len(rows) == 0 {
		return nil
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM drive_weather_series WHERE user_id = $1 AND drive_id = $2`, uid, driveID); err != nil {
		return err
	}
	nf := func(ok bool, v float64) any {
		if !ok {
			return nil
		}
		return v
	}
	ns := func(ok bool, v string) any {
		if !ok {
			return nil
		}
		return v
	}
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO drive_weather_series (
    user_id, drive_id, sampled_at, cadence_minutes, coarse_lat, coarse_lon, provider,
    temp_c, apparent_temp_c, wind_kph, wind_dir_deg, headwind_kph,
    precip_mm, humidity_pct, conditions
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
`, uid, driveID, r.SampledAt.UTC(), r.CadenceMinutes, lat, lon, Provider,
			nf(r.HasTemp, r.TempC),
			nf(r.HasApparent, r.ApparentTempC),
			nf(r.HasWind, r.WindKPH),
			nf(r.HasWind, r.WindDirDeg),
			nf(r.HasHeadwind, r.HeadwindKPH),
			nf(r.HasPrecip, r.PrecipMM),
			nf(r.HasHumidity, r.HumidityPct),
			ns(r.HasConditions, r.Conditions),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Client fetches an hourly snapshot from Open-Meteo's archive API.
// Stateless; safe to share.
type Client struct {
	HTTP    *http.Client
	BaseURL string // override for tests; defaults to https://archive-api.open-meteo.com
}

// NewClient returns a Client with sane defaults.
func NewClient() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		BaseURL: "https://archive-api.open-meteo.com",
	}
}

// FetchHour returns the snapshot for the hour containing `at` at the
// rounded (lat, lon). The trip bearing is used to project wind onto
// a headwind component; pass 0 if unknown (HeadwindKPH stays unset).
func (c *Client) FetchHour(ctx context.Context, lat, lon float64, at time.Time, tripBearingDeg float64, hasBearing bool) (*Snapshot, time.Time, error) {
	clat, clon := Coarsen(lat, lon)
	hour := at.UTC().Truncate(time.Hour)
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(clat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(clon, 'f', 4, 64))
	q.Set("start_date", hour.Format("2006-01-02"))
	q.Set("end_date", hour.Format("2006-01-02"))
	q.Set("hourly", strings.Join([]string{
		"temperature_2m", "apparent_temperature",
		"wind_speed_10m", "wind_direction_10m",
		"precipitation", "relative_humidity_2m",
		"weather_code",
	}, ","))
	q.Set("wind_speed_unit", "kmh")
	q.Set("temperature_unit", "celsius")
	q.Set("precipitation_unit", "mm")
	q.Set("timezone", "UTC")
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/archive?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, hour, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, hour, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, hour, fmt.Errorf("weather: upstream %s", resp.Status)
	}
	var body struct {
		Hourly struct {
			Time        []string  `json:"time"`
			Temperature []float64 `json:"temperature_2m"`
			Apparent    []float64 `json:"apparent_temperature"`
			WindSpeed   []float64 `json:"wind_speed_10m"`
			WindDir     []float64 `json:"wind_direction_10m"`
			Precip      []float64 `json:"precipitation"`
			Humidity    []float64 `json:"relative_humidity_2m"`
			WeatherCode []float64 `json:"weather_code"`
		} `json:"hourly"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, hour, fmt.Errorf("weather: decode: %w", err)
	}
	// Pick the index whose time string matches our truncated hour;
	// fall back to index 0 if Open-Meteo shifted the window slightly.
	target := hour.Format("2006-01-02T15:04")
	idx := 0
	for i, ts := range body.Hourly.Time {
		if ts == target {
			idx = i
			break
		}
	}
	if len(body.Hourly.Time) == 0 {
		return nil, hour, fmt.Errorf("weather: empty response")
	}
	pick := func(arr []float64, i int) (float64, bool) {
		if i < len(arr) {
			return arr[i], true
		}
		return 0, false
	}
	s := &Snapshot{}
	if v, ok := pick(body.Hourly.Temperature, idx); ok {
		s.TempC = v
		s.HasTemp = true
	}
	if v, ok := pick(body.Hourly.Apparent, idx); ok {
		s.ApparentTempC = v
		s.HasApparent = true
	}
	if v, ok := pick(body.Hourly.WindSpeed, idx); ok {
		s.WindKPH = v
		s.HasWind = true
	}
	if v, ok := pick(body.Hourly.WindDir, idx); ok {
		s.WindDirDeg = v
	}
	if v, ok := pick(body.Hourly.Precip, idx); ok {
		s.PrecipMM = v
		s.HasPrecip = true
	}
	if v, ok := pick(body.Hourly.Humidity, idx); ok {
		s.HumidityPct = v
		s.HasHumidity = true
	}
	if v, ok := pick(body.Hourly.WeatherCode, idx); ok {
		s.Conditions = wmoLabel(int(v))
		s.HasConditions = s.Conditions != ""
	}
	if hasBearing && s.HasWind {
		s.HeadwindKPH = Headwind(s.WindKPH, s.WindDirDeg, tripBearingDeg)
		s.HasHeadwind = true
	}
	return s, hour, nil
}

// forecastWindowDays is the cutoff (in days from now) below which we
// use the forecast endpoint with minutely_15 cadence. Open-Meteo
// supports past_days up to 92 on the forecast API; we leave a small
// margin so a drive on the boundary still resolves.
const forecastWindowDays = 80

// FetchRange returns one weather sample per cadence step covering
// [start, end] at the rounded (lat, lon). Picks 15-minute cadence
// from the forecast endpoint when the drive is within the past
// forecastWindowDays, otherwise falls back to hourly archive data.
//
// tripBearingDeg + hasBearing are used to project wind onto a
// per-sample headwind component the same way FetchHour does for
// the start-hour snapshot.
//
// The returned slice is empty (not nil) on a successful upstream
// call that yielded zero usable rows; nil + non-nil error means a
// transport / decode failure that the caller should log.
func (c *Client) FetchRange(ctx context.Context, lat, lon float64, start, end time.Time, tripBearingDeg float64, hasBearing bool) ([]SeriesRow, error) {
	if !end.After(start) {
		return nil, fmt.Errorf("weather: empty time range")
	}
	clat, clon := Coarsen(lat, lon)
	useForecast := time.Since(start) < forecastWindowDays*24*time.Hour
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(clat, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(clon, 'f', 4, 64))
	q.Set("wind_speed_unit", "kmh")
	q.Set("temperature_unit", "celsius")
	q.Set("precipitation_unit", "mm")
	q.Set("timezone", "UTC")
	vars := []string{
		"temperature_2m", "apparent_temperature",
		"wind_speed_10m", "wind_direction_10m",
		"precipitation", "relative_humidity_2m",
		"weather_code",
	}
	var endpoint, blockName string
	var cadence int
	if useForecast {
		blockName = "minutely_15"
		cadence = 15
		// Pad start back to the previous 15-min boundary and end up
		// to the next so the response brackets the drive cleanly.
		s := start.UTC().Truncate(15 * time.Minute)
		e := end.UTC().Add(15*time.Minute - 1).Truncate(15 * time.Minute)
		q.Set("start_date", s.Format("2006-01-02"))
		q.Set("end_date", e.Format("2006-01-02"))
		q.Set("minutely_15", strings.Join(vars, ","))
		endpoint = "https://api.open-meteo.com/v1/forecast?" + q.Encode()
	} else {
		blockName = "hourly"
		cadence = 60
		s := start.UTC().Truncate(time.Hour)
		e := end.UTC().Add(time.Hour - 1).Truncate(time.Hour)
		q.Set("start_date", s.Format("2006-01-02"))
		q.Set("end_date", e.Format("2006-01-02"))
		q.Set("hourly", strings.Join(vars, ","))
		endpoint = strings.TrimRight(c.BaseURL, "/") + "/v1/archive?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("weather: upstream %s", resp.Status)
	}
	// Both endpoints emit the same shape under different block keys
	// (`hourly` vs `minutely_15`). Decode into a generic map so we
	// can switch on blockName without two structs.
	var raw struct {
		Hourly      map[string]json.RawMessage `json:"hourly"`
		Minutely15  map[string]json.RawMessage `json:"minutely_15"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("weather: decode: %w", err)
	}
	block := raw.Hourly
	if blockName == "minutely_15" {
		block = raw.Minutely15
	}
	if block == nil {
		return nil, fmt.Errorf("weather: missing %s block", blockName)
	}
	var times []string
	if err := json.Unmarshal(block["time"], &times); err != nil {
		return nil, fmt.Errorf("weather: decode time: %w", err)
	}
	dec := func(key string) []float64 {
		v, ok := block[key]
		if !ok {
			return nil
		}
		// Open-Meteo emits null entries inside the array for missing
		// metrics; decode through *float64 then collapse.
		var nullable []*float64
		if err := json.Unmarshal(v, &nullable); err != nil {
			return nil
		}
		out := make([]float64, len(nullable))
		for i, p := range nullable {
			if p != nil {
				out[i] = *p
			}
		}
		return out
	}
	temp := dec("temperature_2m")
	app := dec("apparent_temperature")
	wsp := dec("wind_speed_10m")
	wdr := dec("wind_direction_10m")
	prc := dec("precipitation")
	hum := dec("relative_humidity_2m")
	code := dec("weather_code")
	// Track which entries were null so we don't surface zeros as
	// real values. Re-decode once into *float64 form for presence.
	pres := func(key string) []bool {
		v, ok := block[key]
		if !ok {
			return nil
		}
		var nullable []*float64
		if err := json.Unmarshal(v, &nullable); err != nil {
			return nil
		}
		out := make([]bool, len(nullable))
		for i, p := range nullable {
			out[i] = p != nil
		}
		return out
	}
	tempP := pres("temperature_2m")
	appP := pres("apparent_temperature")
	wspP := pres("wind_speed_10m")
	wdrP := pres("wind_direction_10m")
	prcP := pres("precipitation")
	humP := pres("relative_humidity_2m")
	codeP := pres("weather_code")

	rows := make([]SeriesRow, 0, len(times))
	for i, ts := range times {
		// Open-Meteo timestamps are unzoned ISO when timezone=UTC,
		// e.g. "2026-04-12T13:00". Parse as UTC.
		sa, perr := time.Parse("2006-01-02T15:04", ts)
		if perr != nil {
			continue
		}
		// Filter to the drive window. We expand by a single cadence
		// step on each side so the chart has anchors at both edges.
		stepPad := time.Duration(cadence) * time.Minute
		if sa.Before(start.Add(-stepPad)) || sa.After(end.Add(stepPad)) {
			continue
		}
		r := SeriesRow{SampledAt: sa, CadenceMinutes: cadence}
		if i < len(tempP) && tempP[i] {
			r.TempC = temp[i]
			r.HasTemp = true
		}
		if i < len(appP) && appP[i] {
			r.ApparentTempC = app[i]
			r.HasApparent = true
		}
		if i < len(wspP) && wspP[i] {
			r.WindKPH = wsp[i]
			r.HasWind = true
		}
		if i < len(wdrP) && wdrP[i] {
			r.WindDirDeg = wdr[i]
		}
		if i < len(prcP) && prcP[i] {
			r.PrecipMM = prc[i]
			r.HasPrecip = true
		}
		if i < len(humP) && humP[i] {
			r.HumidityPct = hum[i]
			r.HasHumidity = true
		}
		if i < len(codeP) && codeP[i] {
			r.Conditions = wmoLabel(int(code[i]))
			r.HasConditions = r.Conditions != ""
		}
		if hasBearing && r.HasWind {
			r.HeadwindKPH = Headwind(r.WindKPH, r.WindDirDeg, tripBearingDeg)
			r.HasHeadwind = true
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// wmoLabel maps the WMO weather interpretation code Open-Meteo emits
// to a short human label. We collapse the granular gradations
// (light/moderate/heavy) into one phrase per family because the
// recap only needs a vibe, not a precision report.
//
// Code reference: https://open-meteo.com/en/docs (WMO Weather codes).
func wmoLabel(code int) string {
	switch code {
	case 0:
		return "clear sky"
	case 1, 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55, 56, 57:
		return "drizzle"
	case 61, 63, 65, 80, 81, 82:
		return "rain"
	case 66, 67:
		return "freezing rain"
	case 71, 73, 75, 77, 85, 86:
		return "snow"
	case 95, 96, 99:
		return "thunderstorm"
	}
	return ""
}
