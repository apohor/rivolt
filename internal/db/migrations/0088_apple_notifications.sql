-- 0088_apple_notifications.sql: Sign in with Apple server-to-server notifications (architect's design, DA-approved
-- Oct 24).
--
--   apple_notification_seen   replay guard. Apple may resend a notification, and anyone could replay a captured
--                             one inside its freshness window. A row is the sha256 of the notification's signed
--                             payload: no user, no `sub`, nothing that names anyone. Rows older than two days are
--                             swept by retention (the freshness window is an hour).
--   users.email_undeliverable the account's address is an Apple relay address whose forwarding the user turned off
--                             (email-disabled) and may turn on again (email-enabled). Non-essential mail (the
--                             monthly statement, the weekly recap) skips such an address.
--
-- Rollback: DROP TABLE apple_notification_seen; ALTER TABLE users DROP COLUMN email_undeliverable;

CREATE TABLE IF NOT EXISTS apple_notification_seen (
    event_hash BYTEA       PRIMARY KEY,
    seen_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS apple_notification_seen_seen_at ON apple_notification_seen (seen_at);

ALTER TABLE users ADD COLUMN IF NOT EXISTS email_undeliverable BOOLEAN NOT NULL DEFAULT FALSE;
