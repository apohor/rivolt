-- 0021_drive_weather.sql
--
-- Per-drive weather snapshot. Populated lazily during recap
-- generation when the operator opts in (settings: recap.weather_enabled).
-- Decoupled from drive_recaps so a future "weather" stat tile on the
-- drive page can read it without involving the LLM, and so a recap
-- regeneration doesn't re-hit the upstream weather provider.
--
-- Keyed (user_id, drive_id) for the same reasons drive_recaps is:
-- drive_id is unique per user, finished drives are immutable, and
-- ON DELETE CASCADE on user_id keeps the table self-cleaning.
--
-- Coords are intentionally rounded to 0.1 deg before the upstream
-- request — about 11 km of resolution, which matches the upstream
-- grid (Open-Meteo ERA5 ~9 km) and limits the precision of the
-- location disclosure that leaves the box. We persist the rounded
-- pair so cache hits across nearby trips work.

CREATE TABLE IF NOT EXISTS drive_weather (
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    drive_id       TEXT NOT NULL,
    -- Coords rounded to 0.1 deg.
    coarse_lat     DOUBLE PRECISION NOT NULL,
    coarse_lon     DOUBLE PRECISION NOT NULL,
    -- Hour the weather is sampled at, UTC truncated.
    sampled_at     TIMESTAMPTZ NOT NULL,
    -- Provider tag so we can invalidate cleanly if/when we ever
    -- swap upstream (e.g. NOAA NWS for US drives).
    provider       TEXT NOT NULL,
    -- Numeric fields all stored in metric base units; the renderer
    -- converts to F/mph at display time. NULL = upstream omitted
    -- the metric, so the prompt can skip the line cleanly.
    temp_c            DOUBLE PRECISION,
    apparent_temp_c   DOUBLE PRECISION,
    wind_kph          DOUBLE PRECISION,
    wind_dir_deg      DOUBLE PRECISION,
    -- Headwind component along trip bearing in kph; signed (negative
    -- = tailwind). Computed from wind speed/dir + the great-circle
    -- bearing from start->end of the drive.
    headwind_kph      DOUBLE PRECISION,
    precip_mm         DOUBLE PRECISION,
    humidity_pct      DOUBLE PRECISION,
    -- WMO weather code summary -> short label ("light snow",
    -- "clear sky"). Free-form so the SPA can display verbatim.
    conditions        TEXT,
    cached_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, drive_id)
);
