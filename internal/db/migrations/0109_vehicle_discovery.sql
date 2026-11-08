-- 0109_vehicle_discovery.sql: what the owner's vehicle lookup found for a linked account with no vehicle yet (single-owner M4-2b).
--
-- A user with a stored Rivian session and no vehicle row is "looking" until the owner of their group has asked Rivian for their vehicles. That
-- lookup has two answers worth keeping, so the app can tell "we are looking" from "there are none yet" instead of showing a spinner for ever:
--   none   Rivian answered and the account has no vehicle (a reservation, say). Looked at again after next_check_at (a day).
--   error  the lookup failed (Rivian down, a network error). Looked at again after next_check_at (half an hour).
-- No row means nobody has asked yet ("looking"). A lookup that stores vehicles deletes the row. Tenant isolation like every table with a
-- user_id; the row goes with the account (ON DELETE CASCADE).
CREATE TABLE IF NOT EXISTS vehicle_discovery (
    user_id       UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    state         TEXT        NOT NULL CHECK (state IN ('none', 'error')),
    checked_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_check_at TIMESTAMPTZ NOT NULL
);

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['vehicle_discovery'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
