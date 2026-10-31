-- 0099_entitlements_cancel.sql: a paid subscription set to not renew ("cancelled, paid until the period end"). The webhooks (Stripe
-- cancel_at_period_end, StoreKit auto-renew off) set it; the plan card reads it as plan_cancels_at. It does not change what the row
-- grants: the account is Pro until current_period_end either way.
ALTER TABLE entitlements ADD COLUMN IF NOT EXISTS cancel_at_period_end BOOLEAN NOT NULL DEFAULT FALSE;
