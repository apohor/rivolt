-- 0100_account_notices.sql — service notices an account is owed and can see in the app (Rivolt Pro, Sprint 14, item 10).
--
--   account_notices   one row per (account, kind). kind 'launch' is the Terms section 11 notice that Rivolt now has a free tier and Pro.
--                     params holds the dates the notice states (launch, founding_end, terms_effective), fixed when the row is created so a
--                     retried email never states a shorter notice period. shows_until is when the in-app notice stops being shown.
--                     email_claimed_at / email_sent_at are the claim-then-send bookkeeping of the email; email_sent_at is also the
--                     evidence that notice was given, so nothing purges it. dismissed_at hides the banner, never the Settings row.
--
-- Written by the operator CLI (rivolt launch-notice) and by the account's own dismiss; deleted with the account. Tenant row-level
-- security, the 0067 pattern (ENABLE only).
CREATE TABLE IF NOT EXISTS account_notices (
    user_id          UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind             TEXT        NOT NULL CHECK (kind ~ '^[a-z0-9_]{1,40}$'),
    params           JSONB       NOT NULL DEFAULT '{}'::jsonb,
    shows_until      TIMESTAMPTZ NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    email_claimed_at TIMESTAMPTZ,
    email_sent_at    TIMESTAMPTZ,
    dismissed_at     TIMESTAMPTZ,
    PRIMARY KEY (user_id, kind)
);

ALTER TABLE account_notices ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS account_notices_tenant_isolation ON account_notices;
CREATE POLICY account_notices_tenant_isolation ON account_notices
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
