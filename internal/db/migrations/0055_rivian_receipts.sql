-- 0055_rivian_receipts.sql — what Rivian actually billed for a charging
-- session, from the same "Charging history" the owner app shows
-- (getCompletedSessionSummaries; the app displays totalCost verbatim).
--
-- rivian_receipts holds one row per Rivian transaction: the summary
-- fields only (amount, currency, energy, window, vendor, city) plus the
-- detail query's charger name, vendor and isHomeCharger. It never stores
-- payment-method or order-card data, and no street address. charge_id is
-- set when the receipt was matched to one of our charge rows (same
-- window within 5 minutes and similar energy); it is NULL for receipts
-- with no recorded charge (kept so a later match or an export still sees
-- them).
--
-- charges.receipt_* carries the billed amount next to our own estimate:
-- cost / cost_source / price_per_kwh are never overwritten by a receipt,
-- so the estimate and the billed figure stay comparable. NULL = no
-- receipt matched. receipt_source says where it came from.
--
-- Rows cascade with the user (account deletion) and appear in the data
-- export. Metadata-only ADD COLUMNs plus a new table: no rewrite.

CREATE TABLE IF NOT EXISTS rivian_receipts (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    transaction_id   TEXT NOT NULL,
    started_at       TIMESTAMPTZ NOT NULL,
    ended_at         TIMESTAMPTZ NOT NULL,
    total_cost       NUMERIC(12, 4),
    currency         TEXT,
    energy_kwh       DOUBLE PRECISION,
    range_added_km   DOUBLE PRECISION,
    vendor           TEXT,
    city             TEXT,
    charger_name     TEXT,
    is_home_charger  BOOLEAN,
    detail_fetched_at TIMESTAMPTZ,
    charge_id        UUID REFERENCES charges(id) ON DELETE SET NULL,
    matched_at       TIMESTAMPTZ,
    fetched_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, transaction_id)
);
CREATE INDEX IF NOT EXISTS rivian_receipts_user_started ON rivian_receipts (user_id, started_at DESC);
CREATE INDEX IF NOT EXISTS rivian_receipts_charge ON rivian_receipts (charge_id) WHERE charge_id IS NOT NULL;

ALTER TABLE charges
    ADD COLUMN IF NOT EXISTS receipt_cost     NUMERIC(12, 4),
    ADD COLUMN IF NOT EXISTS receipt_currency TEXT,
    ADD COLUMN IF NOT EXISTS receipt_txn_id   TEXT,
    ADD COLUMN IF NOT EXISTS receipt_source   TEXT
        CHECK (receipt_source IN ('rivian_receipt'));

-- rivian_receipt_sync: when each user's receipts were last asked for. It
-- is the cross-pod, cross-restart rate limit ("never in bursts"): a pod
-- claims a user with one conditional upsert before calling Rivian, so two
-- pods or a restart cannot double-call. Operational bookkeeping only (no
-- Rivian data); it cascades with the user.
CREATE TABLE IF NOT EXISTS rivian_receipt_sync (
    user_id          UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    last_attempt_at  TIMESTAMPTZ NOT NULL,
    last_summary_at  TIMESTAMPTZ,
    last_success_at  TIMESTAMPTZ,
    last_error       TEXT
);
