-- 0098_history_floor.sql: the "history deleted before" floor (internal/histfloor). When a user deletes their history, later writers
-- (the receipts syncer, replay catch-up, the drive and charge writers) drop rows that start before it, so deleted history is not
-- re-downloaded. NULL: nothing was deleted. Only ever raised. Never shown or exported; goes with the account (a column of users).
ALTER TABLE users ADD COLUMN IF NOT EXISTS history_deleted_before TIMESTAMPTZ;
