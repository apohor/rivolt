-- 0063_backup_restore_gap.sql — one backup and one restore per user per ten
-- minutes, counted across every replica. The columns hold when the user last
-- started one; a conditional UPDATE claims the slot atomically. Metadata-only
-- ADD COLUMNs.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS last_backup_at  TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS last_restore_at TIMESTAMPTZ;
