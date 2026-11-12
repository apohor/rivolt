-- migrate:no-transaction
-- history import slice 1: see 0117.
CREATE INDEX CONCURRENTLY IF NOT EXISTS charges_import_idx ON charges (import_id) WHERE import_id IS NOT NULL;
