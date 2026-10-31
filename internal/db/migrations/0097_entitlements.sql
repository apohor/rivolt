-- 0097_entitlements.sql — what makes an account Pro (Rivolt Pro, Sprint 14, item 1).
--
--   entitlements   one row per (account, source). source says where the grant comes from: apple / stripe (a paid subscription, written
--                  only by the two billing webhooks), trial (the 14-day trial), founding (the free founding year given to the accounts that
--                  exist at launch) or grant (an operator grant, logged in admin_audit). status: active (Pro while now < current_period_end,
--                  or with no end), past_due (a paid renewal failed: Pro for a 7-day grace after current_period_end) or ended (never Pro:
--                  expired, refunded, revoked). A cancelled subscription stays 'active' until its period end. trial and founding rows
--                  always have an end. external_id is the Apple original transaction id or the Stripe subscription id.
--
-- Nothing in this migration grants anything: the table starts empty and `RIVOLT_BILLING` off (the default, every self-hosted install)
-- makes everyone Pro without reading it. Deleted with the account (ON DELETE CASCADE); the tax-law billing ledger is a separate table
-- that arrives with the Stripe item. Tenant row-level security, the 0067 pattern (ENABLE only).
CREATE TABLE IF NOT EXISTS entitlements (
    user_id            UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    source             TEXT        NOT NULL CHECK (source IN ('apple', 'stripe', 'trial', 'founding', 'grant')),
    plan               TEXT        NOT NULL DEFAULT 'pro' CHECK (plan IN ('pro')),
    status             TEXT        NOT NULL CHECK (status IN ('active', 'past_due', 'ended')),
    current_period_end TIMESTAMPTZ,
    external_id        TEXT        CHECK (external_id IS NULL OR length(external_id) BETWEEN 1 AND 200),
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, source),
    CHECK (source NOT IN ('trial', 'founding') OR current_period_end IS NOT NULL)
);
CREATE UNIQUE INDEX IF NOT EXISTS entitlements_external ON entitlements (source, external_id) WHERE external_id IS NOT NULL;

ALTER TABLE entitlements ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS entitlements_tenant_isolation ON entitlements;
CREATE POLICY entitlements_tenant_isolation ON entitlements
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
