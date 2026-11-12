-- 0114_drop_imports_user_sha.sql: history import slice 1. The full unique index imports_user_sha would refuse a re-import of an undone file;
-- imports_user_sha_live (0113) replaces it. Nothing writes imports today, so no running version depends on the old index. DA ack on the PR.
-- Rollback: CREATE UNIQUE INDEX CONCURRENTLY imports_user_sha ON imports (user_id, source, sha256) WHERE sha256 IS NOT NULL; (only valid while
-- no undone batch shares a file hash with a live one).
SET LOCAL lock_timeout = '5s';
DROP INDEX IF EXISTS imports_user_sha;
