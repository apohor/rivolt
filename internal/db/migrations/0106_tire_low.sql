-- 0106_tire_low.sql: the tire-low alert (an opt-in push when the truck itself reports a tire low).
--
--   push_subscriptions.on_tire_low   the web switch, OFF until the user's own control turns it on
--   push_apns_devices.on_tire_low    the iPhone switch, likewise OFF
--   tire_low_alerts                  one row per alert sent, so a restart or a second pod never sends the same alert
--                                    twice and a repeat for the same wheels waits out the cooldown. wheels is the
--                                    positions named ("fl,rr"): no pressure, no place, no coordinates. episode_started_at
--                                    is when the newest of those wheels was first seen low (a later alert for another wheel is its own row). Rows go with the vehicle
--                                    (and the account), and the job deletes rows older than 30 days.
--
-- Metadata-only ADD COLUMNs plus one new table.
-- Rollback: DROP TABLE tire_low_alerts; ALTER TABLE push_subscriptions DROP COLUMN on_tire_low;
--           ALTER TABLE push_apns_devices DROP COLUMN on_tire_low;

ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS on_tire_low BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE push_apns_devices  ADD COLUMN IF NOT EXISTS on_tire_low BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS tire_low_alerts (
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id         UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    episode_started_at TIMESTAMPTZ NOT NULL,
    wheels             TEXT NOT NULL,
    sent_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (vehicle_id, episode_started_at)
);

CREATE INDEX IF NOT EXISTS tire_low_alerts_sent_idx ON tire_low_alerts (sent_at);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['tire_low_alerts'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
