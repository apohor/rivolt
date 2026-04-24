-- Add per-drive pack-side energy usage, derived from SoC delta × usable
-- pack capacity. Lets the dashboard compute a true pack-side mi/kWh
-- aggregate instead of dividing window miles by charger-delivered kWh
-- (which folds in ~10–15 % charging losses and skews low).
--
-- Backfill uses the per-vehicle vehicles.pack_kwh when set (populated
-- by InferPackKWh during live ingest and by the importer's --pack-kwh
-- flag). Rows whose vehicle has no pack_kwh are left NULL; they'll
-- repopulate on the next re-import or once the vehicle row learns its
-- capacity from the live client.

ALTER TABLE drives
    ADD COLUMN IF NOT EXISTS energy_used_kwh DOUBLE PRECISION;

UPDATE drives d
   SET energy_used_kwh = GREATEST(COALESCE(d.start_soc_pct, 0) - COALESCE(d.end_soc_pct, 0), 0) / 100.0 * v.pack_kwh
  FROM vehicles v
 WHERE v.id = d.vehicle_id
   AND d.energy_used_kwh IS NULL
   AND v.pack_kwh IS NOT NULL
   AND v.pack_kwh > 0
   AND d.start_soc_pct IS NOT NULL
   AND d.end_soc_pct   IS NOT NULL;
