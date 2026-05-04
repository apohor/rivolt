-- 0017_users_disabled.sql
--
-- Per-user enable/disable flag. Auth is OIDC-only, so we can't
-- "delete the password" the way a local-creds install would; the
-- IdP still has the account. What we CAN do is refuse to mint a
-- session for a disabled row, and clear any session cookies on
-- the next request that lands.
--
-- Default false so existing rows stay enabled. The admin endpoint
-- that flips this column refuses to disable the last remaining
-- admin (same guard CountAdmins backs for role demotion).

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE;
