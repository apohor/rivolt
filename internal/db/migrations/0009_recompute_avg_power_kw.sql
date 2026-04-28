-- 0009: recompute avg_power_kw as energy / duration_hours.
--
-- The old recorders averaged charger_power across only the ticks where
-- power was non-zero, which excluded ramp-up and taper and made the
-- value internally inconsistent with energy_added_kwh / duration on
-- the same row. We changed the formula in v0.10.11 but only new rows
-- benefit. This migration backfills every existing row that has both
-- a positive energy_added_kwh and a positive duration.
--
-- We deliberately leave rows with zero / null energy alone — those
-- are rare aborted sessions where any number we'd compute would be
-- noise. Same for sessions whose ended_at is at or before started_at
-- (data corruption from the pre-v0.10.7 phantom-charge bug); the
-- janitor / user-driven delete paths are responsible for those.

UPDATE charges
SET avg_power_kw = energy_added_kwh / (
    EXTRACT(EPOCH FROM (ended_at - started_at)) / 3600.0
)
WHERE energy_added_kwh > 0
  AND ended_at > started_at
  -- Only touch rows whose current avg differs by >5% from the new
  -- formula. Avoids churning rows that were already consistent
  -- (e.g. sessions imported after v0.10.11) and gives migration
  -- replays an idempotency property in practice.
  AND (
    avg_power_kw IS NULL
    OR avg_power_kw = 0
    OR ABS(
      avg_power_kw
      - (energy_added_kwh / (EXTRACT(EPOCH FROM (ended_at - started_at)) / 3600.0))
    ) / NULLIF(avg_power_kw, 0) > 0.05
  );
