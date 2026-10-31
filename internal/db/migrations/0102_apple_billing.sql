-- 0102_apple_billing.sql: what the StoreKit server needs to be idempotent and to map Apple's transactions to accounts (Rivolt Pro, Sprint
-- 14, item 5). Ids and dates only: no card or Apple payment data.
--
--   apple_events         every App Store Server Notification UUID seen: a retried or replayed delivery is acknowledged without being applied
--                        twice. No user column (a notification id says nothing about a user), so no tenant policy.
--   billing_apple_subs   the account a subscription (its originalTransactionId) is linked to, set only by the signed-in account's own report
--                        of a transaction that carries its appAccountToken; last_signed_date is the newest event time applied, so events
--                        that arrive out of order cannot move an entitlement backwards; revoked_at is the event time of a refund or
--                        revoke, after which older events are ignored so they cannot bring Pro back.
CREATE TABLE IF NOT EXISTS apple_events (
    notification_uuid TEXT        PRIMARY KEY CHECK (length(notification_uuid) BETWEEN 1 AND 255),
    type              TEXT        NOT NULL,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS apple_events_received ON apple_events (received_at);

CREATE TABLE IF NOT EXISTS billing_apple_subs (
    original_transaction_id TEXT        PRIMARY KEY CHECK (length(original_transaction_id) BETWEEN 1 AND 255),
    user_id                 UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    last_signed_date        BIGINT      NOT NULL DEFAULT 0,
    revoked_at              BIGINT,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS billing_apple_subs_user ON billing_apple_subs (user_id);

ALTER TABLE billing_apple_subs ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS billing_apple_subs_tenant_isolation ON billing_apple_subs;
CREATE POLICY billing_apple_subs_tenant_isolation ON billing_apple_subs
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
