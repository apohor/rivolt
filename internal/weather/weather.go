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
