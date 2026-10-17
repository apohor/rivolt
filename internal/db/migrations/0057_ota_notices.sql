-- 0057_ota_notices.sql — software-update push alerts.
--
--   push_subscriptions.on_software_update  per-subscription toggle (default on)
--   ota_notices                            one row per (vehicle, kind, version)
--                                          already announced, so a restart or a
--                                          second pod never sends the same alert
--                                          twice. kind is 'available' (a new
--                                          update is offered) or 'installed' (the
--                                          vehicle's software version changed).
--                                          A vehicle's first sync records its
--                                          versions silently (the baseline) so
--                                          nobody gets alerted about old news.
--
-- Metadata-only ADD COLUMN plus one new table.

ALTER TABLE push_subscriptions
    ADD COLUMN IF NOT EXISTS on_software_update BOOLEAN NOT NULL DEFAULT TRUE;

CREATE TABLE IF NOT EXISTS ota_notices (
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rivian_vehicle_id TEXT NOT NULL,
    kind              TEXT NOT NULL CHECK (kind IN ('available', 'installed')),
    version           TEXT NOT NULL,
    baseline          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, rivian_vehicle_id, kind, version)
);
