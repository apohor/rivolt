-- 0083_left_open.sql: the left-open alert (an opt-in push when a door, the frunk, the liftgate, the tailgate or the
-- tonneau stays open after the truck has been parked and left).
--
--   push_subscriptions.on_left_open   the web switch, OFF until the user's own control turns it on
--   push_apns_devices.on_left_open    the iPhone switch, likewise OFF
--   left_open_alerts                  one row per (vehicle, episode) already alerted, so a restart or a second pod
--                                     never sends the same alert twice. An episode is one continuous run of "something
--                                     is open"; episode_started_at is when it was first seen. Holds no closure name, no
--                                     place and no coordinates. Rows go with the vehicle (and the account), and the job
--                                     deletes rows older than 30 days.
--
-- Metadata-only ADD COLUMNs plus one new table.
-- Rollback: DROP TABLE left_open_alerts; ALTER TABLE push_subscriptions DROP COLUMN on_left_open;
--           ALTER TABLE push_apns_devices DROP COLUMN on_left_open;

ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS on_left_open BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE push_apns_devices  ADD COLUMN IF NOT EXISTS on_left_open BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS left_open_alerts (
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id         UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    episode_started_at TIMESTAMPTZ NOT NULL,
    sent_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vehicle_id, episode_started_at)
);

CREATE INDEX IF NOT EXISTS left_open_alerts_sent_idx ON left_open_alerts (sent_at);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['left_open_alerts'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
