-- 0073_archive.sql — bookkeeping for the telemetry archive (N19,
-- telemetry-archive-design.md): monthly vehicle_state objects on the NAS
-- MinIO, encrypted per user, and verified before any month is thinned.

-- One random data key per user, sealed with the app KEK (crypto.Sealer,
-- bound to user_id). Deleting the user deletes the key, which makes every
-- copy of that user's archive objects unreadable (crypto-shredding).
CREATE TABLE IF NOT EXISTS archive_keys (
    user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    sealed_key BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One row per archived (user, vehicle, month) object. verified_at is set only
-- after the object was read back, decrypted and matched the rows written; the
-- thinning and retention gates require it for every (user, vehicle) with rows
-- in the month. needs_rearchive is set when rows land in an archived month
-- (restore, import); rethin_after holds thinning back after an admin restore.
-- vehicle_id has no FK on purpose: the row describes an object that outlives
-- the vehicle row until the deletion job removes it.
CREATE TABLE IF NOT EXISTS archive_months (
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id      UUID NOT NULL,
    month           DATE NOT NULL CHECK (EXTRACT(DAY FROM month) = 1),
    rows            BIGINT NOT NULL,
    first_at        TIMESTAMPTZ NOT NULL,
    last_at         TIMESTAMPTZ NOT NULL,
    sha256_object   TEXT NOT NULL,
    bytes           BIGINT NOT NULL,
    archived_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    verified_at     TIMESTAMPTZ,
    needs_rearchive BOOLEAN NOT NULL DEFAULT FALSE,
    rethin_after    TIMESTAMPTZ,
    PRIMARY KEY (user_id, vehicle_id, month)
);
CREATE INDEX IF NOT EXISTS archive_months_month ON archive_months (month);

-- Outbox of object prefixes to delete (account deletion, history reset). No
-- FK: the row has to outlive the user it names. Rows go 30 days after
-- done_at, so no user id lingers.
CREATE TABLE IF NOT EXISTS archive_deletions (
    id           BIGSERIAL PRIMARY KEY,
    user_id      UUID NOT NULL,
    prefix       TEXT NOT NULL,
    requested_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    attempts     INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT,
    done_at      TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS archive_deletions_pending ON archive_deletions (requested_at) WHERE done_at IS NULL;

-- Tenant RLS, stage 1 like 0059: ENABLE only, so the app role (the owner)
-- is unaffected and the jobs' own predicates stay the filter.
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['archive_keys', 'archive_months', 'archive_deletions'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
