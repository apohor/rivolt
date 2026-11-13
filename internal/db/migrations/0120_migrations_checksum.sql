-- sha256 of each migration file, hex. Nullable: rows written by older binaries have none, and the runner fills them on its next run.
-- A row filled later attests the file as it is TODAY, not the bytes that were applied (see migrate_checksum.go).
SET LOCAL lock_timeout = '2s';
ALTER TABLE migrations ADD COLUMN IF NOT EXISTS checksum TEXT;
