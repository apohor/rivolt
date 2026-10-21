-- 0077_push_apns.sql — Apple push for the iPhone app (iOS design §3).
--
-- push_apns_devices: one row per app install that registered for push. Tied to the
-- app session it registered with (sessions.id, kind 'app'): removing that session
-- removes the device, and a send only goes to devices whose session is still live
-- (not revoked, not expired), so a signed-out phone stops getting pushes even if it
-- never unregistered. The five opt-ins are the web's (push_subscriptions): charging
-- done, anomaly and software update on, plug-in reminder and milestones OFF until
-- the user's own control turns them on. The device token is stored raw (it is
-- needed to send; it is useless without the server's APNs key and the app's
-- topic), never logged, and left out of the data export.
--
-- push_notifications: the text of a notification, kept for 24 hours so the app's
-- notification service extension can fetch it over TLS: Apple's payload carries
-- only an id and a generic line, never the text. Written only when a send goes to
-- at least one APNs device; purged hourly; gone with the account.
--
-- Rollback: DROP TABLE push_notifications; DROP TABLE push_apns_devices.

CREATE TABLE IF NOT EXISTS push_apns_devices (
    id                  UUID PRIMARY KEY,
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id          UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    token               TEXT NOT NULL,
    env                 TEXT NOT NULL CHECK (env IN ('sandbox', 'production')),
    bundle_id           TEXT NOT NULL,
    on_charging_done    BOOLEAN NOT NULL DEFAULT TRUE,
    on_plug_in_reminder BOOLEAN NOT NULL DEFAULT FALSE,
    on_anomaly          BOOLEAN NOT NULL DEFAULT TRUE,
    on_software_update  BOOLEAN NOT NULL DEFAULT TRUE,
    on_milestones       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_ok_at          TIMESTAMPTZ,
    UNIQUE (user_id, token)
);

CREATE INDEX IF NOT EXISTS push_apns_devices_session_idx ON push_apns_devices (session_id);

CREATE TABLE IF NOT EXISTS push_notifications (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL,
    title      TEXT NOT NULL,
    body       TEXT NOT NULL,
    url        TEXT NOT NULL DEFAULT '/',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS push_notifications_created_idx ON push_notifications (created_at);
