-- 0095_trip_recap.sql — opt-in for the saved-trip recap notification.
--
--   engagement_prefs.trip_recap  the user turned on "Trip recap": one notification after they
--                                drive a saved trip, with its planned and actual arrival charge
--                                and cost. Off for everyone until they choose it. Account-level
--                                like weekly_recap; it goes to the devices that have
--                                notifications on.
--
-- Metadata-only ADD COLUMN, no rewrite.
ALTER TABLE engagement_prefs ADD COLUMN IF NOT EXISTS trip_recap BOOLEAN NOT NULL DEFAULT FALSE;
