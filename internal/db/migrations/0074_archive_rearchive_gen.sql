-- 0074_archive_rearchive_at.sql — a counter of re-archive flags on each
-- archive_months row. Writers that add rows to an archived month bump it in
-- their own transaction (archive.MarkRearchive); the archive job reads it in
-- the snapshot it archives from and clears needs_rearchive only if it is
-- unchanged when the job records the object, so a write it couldn't see
-- (still uncommitted, or later) keeps the month flagged. A timestamp can't
-- order an uncommitted writer (DA on #628).
ALTER TABLE archive_months ADD COLUMN IF NOT EXISTS rearchive_gen BIGINT NOT NULL DEFAULT 0;
