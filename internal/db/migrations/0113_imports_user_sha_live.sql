-- migrate:no-transaction
-- history import slice 1: the same file can be imported again after Undo, so the uniqueness of (user, source, sha256) only holds for batches
-- that are not undone (design R1). The new partial index is built first; 0114 drops the old full one.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS imports_user_sha_live ON imports (user_id, source, sha256) WHERE sha256 IS NOT NULL AND undone_at IS NULL;
