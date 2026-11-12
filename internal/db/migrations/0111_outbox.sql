-- 0111_outbox.sql: the transactional outbox (S19 8 slice 1; internal/outbox; design: zero-downtime-architecture.md section 2.3).
--
-- A discrete side effect (a close hook, a push, an email, a milestone) is recorded as a row in the SAME transaction as the change that causes it,
-- so a process move between the change and the send loses nothing and sends nothing twice from the recorder's side. A sender (a later slice) claims
-- rows with FOR UPDATE SKIP LOCKED and a lease, sends, then marks them. Delivery is at-least-once; each effect keeps its own claim as the dedupe.
--   key            built by the caller: (vehicle, session id, hook kind, seq); UNIQUE, so the insert is idempotent (ON CONFLICT DO NOTHING)
--   vehicle_id     null for an account-level kind (the account-level key carries the user id)
--   payload        ids and kinds only, never coordinates, addresses or names; at most 4 KB
--   claimed_*      the sender's lease; a Release sets claimed_until to now() + backoff so the claim query honours it
--   failed_at      set when attempts reaches the cap; the row is never claimed again and is kept 30 days (sent rows 7 days)
-- Service-only: the request role has no grant (appDeniedTables); tenant policy by user_id like every user table. Rows go with the user and the vehicle.
-- A new table nobody else can see yet: no lock on anything that exists, no destructive step. Nothing reads or writes it in this slice.
-- The FOREIGN KEYs on users and vehicles take a short SHARE ROW EXCLUSIVE lock on each while the table is created; a bounded lock_timeout (SET LOCAL:
-- each migration runs in its own transaction) turns a stuck lock into a failed, retried migration instead of a queue behind it.
-- Rollback: DROP TABLE outbox;
SET LOCAL lock_timeout = '5s';
CREATE TABLE IF NOT EXISTS outbox (
    id            BIGSERIAL   PRIMARY KEY,
    key           TEXT        NOT NULL UNIQUE CHECK (char_length(key) BETWEEN 1 AND 200),
    user_id       UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id    UUID        REFERENCES vehicles(id) ON DELETE CASCADE,
    kind          TEXT        NOT NULL,
    payload       JSONB       NOT NULL DEFAULT '{}' CHECK (octet_length(payload::text) <= 4096),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_by    TEXT,
    claimed_until TIMESTAMPTZ,
    attempts      INTEGER     NOT NULL DEFAULT 0,
    sent_at       TIMESTAMPTZ,
    failed_at     TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS outbox_pending_idx ON outbox (claimed_until NULLS FIRST, id) WHERE sent_at IS NULL AND failed_at IS NULL;
CREATE INDEX IF NOT EXISTS outbox_sent_idx    ON outbox (sent_at)   WHERE sent_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS outbox_failed_idx  ON outbox (failed_at) WHERE failed_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS outbox_user_idx    ON outbox (user_id);
CREATE INDEX IF NOT EXISTS outbox_vehicle_idx ON outbox (vehicle_id) WHERE vehicle_id IS NOT NULL;

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['outbox'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
