-- 0126_apple_sandbox_review.sql: App Review buys in Apple's Sandbox against the production server (design: apple-iap-plan.md, DA-ruled).
--   entitlements.sandbox, billing_apple_subs.sandbox   a Sandbox-signed purchase by the review account (named in the deployment's values, not
--                                                       here): short-lived, never counted, never billing.
-- Independent of the other migrations (the runner applies every unapplied file in name order, so 0125 may land before or after).
-- Rollback: ALTER TABLE entitlements DROP COLUMN sandbox; ALTER TABLE billing_apple_subs DROP COLUMN sandbox;
SET LOCAL lock_timeout = '2s';

ALTER TABLE entitlements       ADD COLUMN IF NOT EXISTS sandbox BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE billing_apple_subs ADD COLUMN IF NOT EXISTS sandbox BOOLEAN NOT NULL DEFAULT FALSE;
