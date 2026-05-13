-- 0029_signup_tokens.sql
--
-- Magic-link signup. Approve mints a single-use token bound to the
-- email + an expiry; the requester completes signup at
--    https://rivolt.dev/signup?token=<token>
-- which prefills the email server-side and only asks for a password
-- (and an optional display name).
--
-- Token column is UNIQUE so a stray collision can't grant access to
-- the wrong row, and the partial index makes lookups by token fast
-- without indexing the NULL rows from before this migration shipped.
--
-- invite_code stays on the row for backward compatibility — codes
-- already distributed before the token flow are still redeemable at
-- /signup until they're used. New approvals only ever set the token.
ALTER TABLE signup_requests
    ADD COLUMN IF NOT EXISTS signup_token       TEXT,
    ADD COLUMN IF NOT EXISTS token_expires_at   TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS token_used_at      TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS signup_requests_signup_token
    ON signup_requests (signup_token)
    WHERE signup_token IS NOT NULL;
