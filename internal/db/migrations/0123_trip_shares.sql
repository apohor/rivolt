-- 0123_trip_shares.sql
--
-- Public read-only trip links (Share a trip 1). A row is one link: a frozen, sanitized copy of a saved trip's plan (snapshot) and
-- the SHA-256 of the link's token. The token itself is never stored. Design: _shared/share-trip-design.md, approved by DA with
-- conditions S1-S9.
--
-- cut_radius_m is the per-link random radius (3 to 6 km) the route was cut by around the start, the destination and every via; it is
-- stored for the record and never returned to anyone.
-- A bounded lock_timeout (SET LOCAL: each migration runs in its own transaction) makes a busy users or saved_trips fail the migration, which
-- retries, instead of queueing behind a long query and blocking every reader of the table behind it.
SET LOCAL lock_timeout = '2s';

CREATE TABLE IF NOT EXISTS trip_shares (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    saved_trip_id UUID        NOT NULL REFERENCES saved_trips(id) ON DELETE CASCADE,
    token_hash    BYTEA       NOT NULL UNIQUE,
    title         TEXT        NOT NULL,
    cut_radius_m  INTEGER     NOT NULL,
    snapshot      JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at    TIMESTAMPTZ,
    revoked_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS trip_shares_user_created ON trip_shares (user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS trip_shares_saved_trip ON trip_shares (saved_trip_id);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['trip_shares'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;

-- The public read: the snapshot of a live link, by token hash, and nothing else (not the owner, not the radius). It runs with the
-- owner's rights because the visitor has no user for the tenant policy. PUBLIC loses the default EXECUTE (as for 0108's functions):
-- the request role cannot call it, only the owner-role pool the public handler uses.
CREATE OR REPLACE FUNCTION trip_share_get(p_hash bytea)
RETURNS jsonb LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public AS $$
    SELECT snapshot FROM trip_shares
     WHERE token_hash = p_hash AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())
$$;

REVOKE ALL ON FUNCTION trip_share_get(bytea) FROM PUBLIC;
