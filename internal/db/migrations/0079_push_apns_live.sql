-- 0079_push_apns_live.sql — the charging Live Activity, registration only (no
-- sending yet; that comes with RIVOLT_APNS_LIVE).
--
-- push_apns_devices.on_charging_live: a per-iPhone switch, OFF until the user's own
-- control turns it on (its label says Apple can see charge level and power).
--
-- push_apns_activities: the per-activity push token ActivityKit gives the app for
-- one running Live Activity, tied to the device it runs on (so a signed-out phone,
-- whose device row is deleted, loses its activities with it) and to a vehicle. The
-- app starts the activity itself (no push-to-start), registers the token, and the
-- row is deleted when the activity ends, when the switch is turned off, and at the
-- latest 12 hours after it started (an activity lives 8 to 12 hours). started_at
-- is never moved by a re-registration, so the 12 hours can't be extended.
--
-- Rollback: DROP TABLE push_apns_activities;
--           ALTER TABLE push_apns_devices DROP COLUMN on_charging_live;

ALTER TABLE push_apns_devices ADD COLUMN IF NOT EXISTS on_charging_live BOOLEAN NOT NULL DEFAULT FALSE;

CREATE TABLE IF NOT EXISTS push_apns_activities (
    id           UUID PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_id    UUID NOT NULL REFERENCES push_apns_devices(id) ON DELETE CASCADE,
    vehicle_id   UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    token        TEXT NOT NULL,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_sent_at TIMESTAMPTZ,
    UNIQUE (user_id, token)
);

CREATE INDEX IF NOT EXISTS push_apns_activities_device_idx ON push_apns_activities (device_id);
CREATE INDEX IF NOT EXISTS push_apns_activities_started_idx ON push_apns_activities (started_at);

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
ALTER TABLE push_apns_activities ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS push_apns_activities_tenant_isolation ON push_apns_activities;
CREATE POLICY push_apns_activities_tenant_isolation ON push_apns_activities
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
