-- 0019_vehicle_state_altitude.sql
--
-- Adds an altitude (meters above sea level) column to vehicle_state.
-- Populated by the live recorder via the elevation resolver
-- (Mapzen Terrarium DEM) when a sample carries a usable lat/lon.
--
-- Nullable: legacy rows written before this migration, ElectraFi
-- imports, samples that arrived without a GPS fix, and the
-- transient cold-cache misses on a new tile all leave it NULL.
-- The frontend Elevation chart hides itself when every value is
-- null so legacy drives don't render an empty panel.

ALTER TABLE vehicle_state
    ADD COLUMN IF NOT EXISTS altitude_m REAL;
