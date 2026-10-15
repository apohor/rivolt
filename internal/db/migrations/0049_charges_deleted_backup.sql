-- 0049_charges_deleted_backup.sql: undo log for `rivolt repair-phantom-charges`.
--
-- The repair deletes stale/phantom live charges by id (architect's
-- scratchpad/phantom-charges.md §4). Before it deletes anything it copies
-- each row, and each row that would cascade away with it (pack-health
-- samples, place restamps), into this table in the SAME transaction, so a
-- batch can be restored exactly (`--restore <batch_id>`).
--
-- Kept until the owner decides to drop it (at least 90 days).
--   kind   charge | pack_health_sample | charge_restamp
--   row    row_to_json of the deleted row (jsonb_populate_record restores it)
--   restored_at set when a batch is restored; a restored batch is refused

CREATE TABLE IF NOT EXISTS charges_deleted_backup (
    batch_id    UUID        NOT NULL,
    kind        TEXT        NOT NULL CHECK (kind IN ('charge', 'pack_health_sample', 'charge_restamp')),
    charge_id   UUID        NOT NULL,
    user_id     UUID        NOT NULL,
    row         JSONB       NOT NULL,
    reason      TEXT,
    deleted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    restored_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS charges_deleted_backup_batch ON charges_deleted_backup (batch_id);
