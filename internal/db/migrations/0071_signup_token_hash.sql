-- 0071_signup_token_hash.sql — signup magic-link tokens are stored as their hash.
--
-- signup_requests.signup_token held the token itself, so a database dump, a backup
-- or any read of the table (an admin list returned it) yielded a usable 14-day
-- invite. The application now stores lower-case hex SHA-256 of the token (64
-- characters; a token is 26) and looks a presented token up by its hash. This
-- converts the rows that still hold a plain token. The link already emailed keeps
-- working: the presented token is hashed before the lookup.
--
-- Idempotent (only rows whose value is not already 64 characters change).
-- Rollback: the plain tokens cannot be recovered from the hashes, and the old code
-- looks a presented token up unhashed, so after a rollback the outstanding invites
-- stop working until they are re-approved (no data is lost).
UPDATE signup_requests
   SET signup_token = encode(sha256(convert_to(signup_token, 'UTF8')), 'hex')
 WHERE signup_token IS NOT NULL AND length(signup_token) <> 64;
