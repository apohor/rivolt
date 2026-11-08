-- 0107_charges_capped_backup.sql: undo log for `rivolt repair-charge-stretched` (#1630).
--
-- The repair caps ended_at on charge rows the stale-charge janitor stretched past their real end. Before it
-- changes a row it copies the whole old row here, in the SAME transaction, so a batch can be restored exactly
-- (`--restore BATCH`). A split also creates rows (created_ids); the undo deletes exactly those.
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
    created_ids UUID[]      NOT NULL DEFAULT '{}',
    capped_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    restored_at TIMESTAMPTZ,
    PRIMARY KEY (batch_id, charge_id)
);

CREATE INDEX IF NOT EXISTS charges_capped_backup_charge ON charges_capped_backup (charge_id);

CREATE INDEX IF NOT EXISTS charges_capped_backup_user ON charges_capped_backup (user_id);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS). The rows hold whole charges
-- (coordinates included); account deletion purges them (db.DeleteUser, db.DeleteUserAccount) and so does a history cutoff.
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['charges_capped_backup'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
