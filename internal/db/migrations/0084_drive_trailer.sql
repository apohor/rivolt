-- 0084_drive_trailer.sql: whether a trailer was connected on a drive (Sprint 8 item 7).
--
--   drives.trailer           what the recorder saw: true when the trailer connector read "present" for at least half
--                            of the time its state was known during the drive, false when it was known and below
--                            that, NULL when it was never known (an old drive, or a truck that does not send it).
--                            The signal is the connector, not the load: a bike rack or a cargo carrier with lights
--                            plugged into the 7-pin can read "present".
--   drives.trailer_override  the user's own correction (true or false), NULL when they have made none. The effective
--                            value is COALESCE(trailer_override, trailer). Re-recording or repairing a drive never
--                            touches it.
--
-- Additive; old drives stay NULL (nothing records the history to backfill from). Carried by backup, restore and
-- export with the rest of the drive. Rollback: ALTER TABLE drives DROP COLUMN trailer, DROP COLUMN trailer_override;

ALTER TABLE drives ADD COLUMN IF NOT EXISTS trailer BOOLEAN;
ALTER TABLE drives ADD COLUMN IF NOT EXISTS trailer_override BOOLEAN;
