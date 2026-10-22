-- 0081_charge_overrides.sql — the user's own word on a charge (GitHub #906 part 2).
--
-- place_override: "home" or "away", set by the user on the charge's page. When set, place
-- holds the same value, and nothing re-classifies the row: re-checking past charges against
-- a new home skips it, undoing a re-check leaves it, and the recorder's re-saves keep it.
-- NULL means automatic (place follows the home location as before).
--
-- location_override: "recorded" when the user says the charge happened where the truck was
-- (its stored lat/lon), not at the nearest public charger: maps pin the recorded position and
-- never snap. No coordinates are added; it's a flag on the existing lat/lon.
--
-- Rollback: ALTER TABLE charges DROP COLUMN place_override, DROP COLUMN location_override;

ALTER TABLE charges ADD COLUMN IF NOT EXISTS place_override TEXT
    CHECK (place_override IN ('home', 'away'));
ALTER TABLE charges ADD COLUMN IF NOT EXISTS location_override TEXT
    CHECK (location_override = 'recorded');
