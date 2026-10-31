-- 0104_stripe_sub_terms.sql: what a Stripe subscription costs and how it renews, kept beside the entitlement so the transactional billing
-- emails (the purchase confirmation with its renewal terms, the yearly-renewal reminder) can state it without calling Stripe. Written by the
-- webhook from the subscription's price. Additive: nullable columns, a default on the flag.
ALTER TABLE billing_stripe_subs
    ADD COLUMN IF NOT EXISTS price_interval TEXT,
    ADD COLUMN IF NOT EXISTS unit_amount    BIGINT,
    ADD COLUMN IF NOT EXISTS currency       TEXT,
    ADD COLUMN IF NOT EXISTS trialing       BOOLEAN NOT NULL DEFAULT FALSE;
