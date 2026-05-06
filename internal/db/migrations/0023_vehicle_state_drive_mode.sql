-- 0023_vehicle_state_drive_mode.sql
--
-- Adds a drive_mode column to vehicle_state to record the drive mode
-- (e.g., "everyday", "sport", "all-terrain") when samples are recorded.
-- Populated by the live recorder from rivian.State.DriveMode.
--
-- Nullable: legacy rows written before this migration, ElectraFi
-- imports, and samples from vehicles that don't report drive_mode
-- will have NULL. The frontend charts show mode/gear bands when
-- data is available, falling back to gear if mode is missing.

ALTER TABLE vehicle_state
    ADD COLUMN IF NOT EXISTS drive_mode TEXT;
