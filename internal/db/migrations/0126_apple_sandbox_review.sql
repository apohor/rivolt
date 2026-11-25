-- 0126_apple_sandbox_review.sql: App Review buys in Apple's Sandbox against the production server (design: apple-iap-plan.md, DA-ruled).
--   apple_sandbox_accounts   the accounts (the review account only; operator-filled) whose Sandbox transactions production may accept,
--                            and which see the purchase UI while a version is in review.
--   entitlements.sandbox, billing_apple_subs.sandbox   a Sandbox-signed purchase: short-lived, never counted, never billing.
-- Rollback: DROP TABLE apple_sandbox_accounts; ALTER TABLE entitlements DROP COLUMN sandbox; ALTER TABLE billing_apple_subs DROP COLUMN sandbox;
SET LOCAL lock_timeout = '2s';

CREATE TABLE IF NOT EXISTS apple_sandbox_accounts (
    user_id    UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE entitlements       ADD COLUMN IF NOT EXISTS sandbox BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE billing_apple_subs ADD COLUMN IF NOT EXISTS sandbox BOOLEAN NOT NULL DEFAULT FALSE;

-- Tenant isolation, like every table with a user_id (TestEveryUserTableHasTenantRLS).
DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['apple_sandbox_accounts'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;
