-- 0037_charges_active_seconds.sql — real charging time per session.
--
-- charges.ended_at - started_at spans the whole plugged-in period,
-- including charging_ready / battery-conditioning idle after the target
-- SoC is reached (an overnight L2 session can read as 20+ hours). What
-- the user wants displayed is the time actually spent charging.
--
-- The live recorder accumulates this in liveCharge.activeSeconds using
-- the same gated dt that feeds the energy integral (dt only counts while
-- charger power is above chargingPowerFloorKW, capped at
-- maxIntegrationGap), so idle stretches don't inflate it. It's persisted
-- here rather than derived at read time so the charges list doesn't have
-- to fan out to per-session samples.
--
-- Nullable, no backfill: legacy rows and non-live sources (ElectraFi
-- import) leave it NULL; the UI falls back to the ended_at - started_at
-- wall span when it's absent/zero. New live sessions populate it going
-- forward.

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS active_seconds DOUBLE PRECISION;
