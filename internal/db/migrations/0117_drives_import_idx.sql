-- migrate:no-transaction
-- history import slice 1: Undo deletes a batch by import_id, and deleting an imports row looks its drives up by it (ON DELETE SET NULL). Partial: live rows have none.
CREATE INDEX CONCURRENTLY IF NOT EXISTS drives_import_idx ON drives (import_id) WHERE import_id IS NOT NULL;
