-- 0018_drives_route_polyline.sql
--
-- Adds an encoded route polyline (Google polyline algorithm,
-- precision-5 for ~1m resolution) per drive. Set by the live
-- recorder when a drive closes, used by the drives overview map
-- to draw real on-road routes instead of straight lines between
-- start/end markers.
--
-- Nullable: legacy ElectraFi-imported drives and pre-migration
-- live drives won't have one until backfilled. The frontend
-- falls back to a straight start->end line when null, so the
-- column being absent never breaks the overview.

ALTER TABLE drives
    ADD COLUMN IF NOT EXISTS route_polyline TEXT;
