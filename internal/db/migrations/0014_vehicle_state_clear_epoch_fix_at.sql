-- 0014_vehicle_state_clear_epoch_fix_at.sql
--
-- Cleanup: zero out any `location_fix_at` value that pre-dates 2010.
--
-- An earlier release introduced `vehicle_state.location_fix_at`
-- but the Sample struct used `time.Time` (not `*time.Time`).
-- encoding/json's omitempty does not honor zero struct values, so
-- the live recorder wrote the Go zero value ("0001-01-01T00:00:00Z")
-- to the database whenever the gateway didn't include a GNSS @defer
-- slice in the WS frame. The frontend then computed a ~17 million-hour
-- fix age and rendered an absurd "GPS fix 17753748h 32m stale" badge.
--
-- The recorder has since been fixed (pointer field + nil check), so
-- going forward we'll only persist real fix timestamps. This
-- migration normalizes the bad rows already written.
--
-- 2010 is a generous floor: Rivian was founded in 2009 and shipped
-- its first vehicle in 2021, so any fix timestamp older than that
-- is unambiguously a zero-time artifact rather than a genuinely
-- stuck GNSS module.
UPDATE vehicle_state
   SET location_fix_at = NULL
 WHERE location_fix_at IS NOT NULL
   AND location_fix_at < TIMESTAMPTZ '2010-01-01';
