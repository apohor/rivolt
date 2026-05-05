-- 0022_drive_weather_series.sql
--
-- Per-drive weather time series backing the drive-detail weather
-- panel. Cadence varies per row by source:
--   * 15 minutes when the drive falls inside Open-Meteo's forecast
--     window (past_days <= ~90), which serves recent drives via
--     minutely_15.
--   * 60 minutes when we fall back to the archive API (ERA5
--     reanalysis), which is hourly-only.
-- We persist `cadence_minutes` rather than splitting the table so
-- the SPA can render either uniformly and so a future provider
-- swap stays a one-column change.
--
-- The single-hour `drive_weather` row remains the source of truth
-- for the recap prompt and the at-a-glance "Weather at start"
-- strip; this table is purely the time-series companion.
--
-- Keyed (user_id, drive_id, sampled_at): one row per sample the
-- drive overlaps. Cascades on user delete the same way the
-- summary table does. No FK/cascade between drive_weather and
-- drive_weather_series -- the SPA reads them independently and an
-- imbalance surfaces as a missing chart line, not a crash.
--
-- Same coarsened-coords disclosure rules as drive_weather: lat/lon
-- rounded to 0.1 deg before the upstream request, persisted so
-- cache hits across nearby drives still work.

CREATE TABLE IF NOT EXISTS drive_weather_series (
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    drive_id       TEXT NOT NULL,
    -- UTC timestamp of the sample. Truncated to the cadence step
    -- (15-min boundary or hour boundary) so re-fetching with a
    -- different `at` doesn't produce duplicates.
    sampled_at     TIMESTAMPTZ NOT NULL,
    -- 15 or 60. Lets the renderer pick the right step shape for
    -- precipitation (which is an accumulation, not an instant).
    cadence_minutes SMALLINT NOT NULL,
    coarse_lat     DOUBLE PRECISION NOT NULL,
    coarse_lon     DOUBLE PRECISION NOT NULL,
    provider       TEXT NOT NULL,
    -- Metric base units, same as drive_weather. Renderer converts.
    -- NULL = upstream omitted; the SPA skips that sample for that
    -- series rather than guessing.
    temp_c            DOUBLE PRECISION,
    apparent_temp_c   DOUBLE PRECISION,
    wind_kph          DOUBLE PRECISION,
    wind_dir_deg      DOUBLE PRECISION,
    headwind_kph      DOUBLE PRECISION,
    precip_mm         DOUBLE PRECISION,
    humidity_pct      DOUBLE PRECISION,
    conditions        TEXT,
    cached_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, drive_id, sampled_at)
);

-- Reads are always (user_id, drive_id) ordered by sampled_at. The
-- PK already covers it.
