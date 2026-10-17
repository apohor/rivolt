-- 0062_onboarding_emails.sql — welcome email and opt-out for the setup emails.
--
--   welcome_sent_at          when the welcome email was claimed (sent once).
--                            Every account that exists at migration time is
--                            marked, so nobody gets a retroactive welcome.
--   engagement_emails_off_at when the user turned off the welcome and setup
--                            reminder emails (Settings, or the one-click link
--                            in the email). NULL = on. Account-security mail
--                            (re-auth, deletion warnings, breach notices) and
--                            the monthly statement are not affected.
--
-- Metadata-only ADD COLUMNs plus one UPDATE of the existing accounts.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS welcome_sent_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS engagement_emails_off_at TIMESTAMPTZ;

UPDATE users SET welcome_sent_at = NOW() WHERE welcome_sent_at IS NULL;
