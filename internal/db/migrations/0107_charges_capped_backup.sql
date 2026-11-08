-- 0107_charges_capped_backup.sql: undo log for `rivolt repair-charge-stretched` (#1630).
--
-- The repair caps ended_at on charge rows the stale-charge janitor stretched past their real end. Before it
-- changes a row it copies the whole old row here, in the SAME transaction, so a batch can be restored exactly
-- (`--restore BATCH`).
--   row         to_jsonb of the charge row before the cap
--   capped_to   the ended_at the repair wrote
--   restored_at set when a batch is restored; a restored batch is refused
CREATE TABLE IF NOT EXISTS charges_capped_backup (
    batch_id    UUID        NOT NULL,
    charge_id   UUID        NOT NULL,
    user_id     UUID        NOT NULL,
    row         JSONB       NOT NULL,
    capped_to   TIMESTAMPTZ NOT NULL,
    reason      TEXT,
    capped_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    restored_at TIMESTAMPTZ,
    PRIMARY KEY (batch_id, charge_id)
);

CREATE INDEX IF NOT EXISTS charges_capped_backup_charge ON charges_capped_backup (charge_id);
