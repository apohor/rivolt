-- 0054_charges_deleted_backup_user_idx.sql: index the repair undo log by user.
--
-- charges_deleted_backup (0049) is only indexed by batch_id, but account
-- deletion removes a user's rows with WHERE user_id = $1 (db/users.go), a
-- full scan. The table is small and written rarely (only by rivolt
-- repair-phantom-charges), but CREATE INDEX takes a SHARE lock that
-- blocks those writes, so bound the wait: a busy table fails this
-- migration, which retries on the next boot, instead of queueing writers.
SET LOCAL lock_timeout = '5s';

CREATE INDEX IF NOT EXISTS charges_deleted_backup_user ON charges_deleted_backup (user_id);
