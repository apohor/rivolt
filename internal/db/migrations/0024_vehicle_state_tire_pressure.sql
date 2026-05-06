-- 0024_vehicle_state_tire_pressure.sql
--
-- Persists tire pressure (minimum of the four corners) into
-- vehicle_state. Stored in bar (Rivian's native unit). The
-- efficiency analyzer pulls the median value across a drive's
-- samples to factor under-inflation into its breakdown.
--
-- Nullable: legacy rows and ElectraFi imports have no pressure
-- data and stay NULL. Storing the min instead of all four
-- corners trades a tiny amount of fidelity for half the storage
-- and a single index-friendly column; under-inflation is what
-- matters for efficiency, and the worst tire dominates.

ALTER TABLE vehicle_state
    ADD COLUMN IF NOT EXISTS tire_pressure_min_bar REAL;
