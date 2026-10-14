-- 0044_charges_place.sql — record where a charge happened (home / away)
-- when it's recorded, instead of re-deriving it from the current home
-- location every time it's read.
--
-- Rationale:
--
--   Since the away-AC decision (A5, 2026-10-13), "AC charging away from
--   home is unknown until priced". The ledger decided home vs away from
--   the user's *current* saved home, so a user who moves (or corrects
--   their home pin) would see every past home-rate charge at the old
--   address flip to "needs a price". The recorder now stamps place at
--   record time; the ledger uses it when present.
--
-- Backfill:
--
--   Existing rows are classified once, here, from each user's current
--   home location (250 m radius, same as rivian.HomeRadiusM and the
--   ledger). That freezes today's answer, so a later move doesn't change
--   history. Rows without a position, or users without a saved home,
--   stay NULL and keep the ledger's read-time derivation.

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS place TEXT CHECK (place IN ('home', 'away'));

WITH homes AS (
    SELECT user_id,
           (value->>'home_location.latitude')::float8  AS lat,
           (value->>'home_location.longitude')::float8 AS lon
      FROM user_settings
     WHERE value ? 'home_location.latitude'
       AND value->>'home_location.latitude'  ~ '^-?[0-9]+(\.[0-9]+)?$'
       AND value->>'home_location.longitude' ~ '^-?[0-9]+(\.[0-9]+)?$'
)
UPDATE charges c
   SET place = CASE
       WHEN 2 * 6371000 * asin(sqrt(
                power(sin(radians(c.lat - h.lat) / 2), 2) +
                cos(radians(h.lat)) * cos(radians(c.lat)) *
                power(sin(radians(c.lon - h.lon) / 2), 2))) <= 250
       THEN 'home' ELSE 'away' END
  FROM homes h
 WHERE c.user_id = h.user_id
   AND c.place IS NULL
   AND c.lat IS NOT NULL AND c.lon IS NOT NULL
   AND NOT (c.lat = 0 AND c.lon = 0);
