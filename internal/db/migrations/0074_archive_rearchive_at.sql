-- 0074_archive_rearchive_at.sql — when a month was last flagged for
-- re-archiving (rows landed in an archived month: restore, import, replay).
-- The archive job clears needs_rearchive only if no flag came after it
-- started reading, so a write during a re-archive isn't forgotten.
ALTER TABLE archive_months ADD COLUMN IF NOT EXISTS rearchive_requested_at TIMESTAMPTZ;
