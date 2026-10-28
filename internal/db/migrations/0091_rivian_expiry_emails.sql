-- 0091_rivian_expiry_emails.sql — opt-out for the Rivian session expiry reminders.
--
--   rivian_expiry_emails_off_at  when the user stopped the reminders that say their Rivian
--                                connection is about to expire (the one-click link in the
--                                email, or Settings). NULL = on. Independent of the welcome
--                                and setup emails, the monthly statement, and account-security
--                                mail (re-auth, deletion warnings, breach notices).
--
-- Metadata-only ADD COLUMN, no rewrite.
ALTER TABLE users ADD COLUMN IF NOT EXISTS rivian_expiry_emails_off_at TIMESTAMPTZ;
