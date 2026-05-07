-- 0015_users_role.sql
--
-- Add a role column to users for the admin track. Two values today:
--   'user'   default; per-user data plane only
--   'admin'  in addition can manage other users + global app_settings
--
-- Bootstrap: the first user inserted into an empty table is
-- promoted to admin via a partial unique-friendly trigger. After
-- one admin exists, new inserts default to 'user'. Operators can
-- still pre-promote a specific email by setting the
-- RIVOLT_BOOTSTRAP_ADMIN_EMAIL env, which the application layer
-- consults during EnsureUserFull (so an OIDC user with that email
-- claim gets minted as admin, even if a placeholder admin already
-- exists). The trigger is the failsafe — application logic is the
-- intent.
--
-- 'role' was deliberately not made an ENUM: enums require an ALTER
-- TYPE round trip to add values, and we anticipate adding
-- 'read-only' / 'service' down the line.

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS role TEXT NOT NULL DEFAULT 'user'
    CHECK (role IN ('user', 'admin'));

-- Helpful index for "list all admins" — admin count is small,
-- partial index keeps it tiny.
CREATE INDEX IF NOT EXISTS users_role_admin_idx
  ON users (role)
  WHERE role = 'admin';

-- Bootstrap trigger: when a user is inserted and there is currently
-- no admin in the table, promote the new row before the row is
-- written. Implemented as BEFORE INSERT so the first row can be
-- created with role='user' (the application default) and still
-- end up admin.
CREATE OR REPLACE FUNCTION users_bootstrap_admin()
RETURNS TRIGGER
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.role = 'user'
     AND NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin') THEN
    NEW.role := 'admin';
  END IF;
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS users_bootstrap_admin_trg ON users;
CREATE TRIGGER users_bootstrap_admin_trg
BEFORE INSERT ON users
FOR EACH ROW
EXECUTE FUNCTION users_bootstrap_admin();

-- Backfill: if there's already at least one user and no admin,
-- promote the oldest user. This is the install-upgrade path for
-- pre-existing multi-user installs that predate the admin role.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM users WHERE role = 'admin')
     AND     EXISTS (SELECT 1 FROM users) THEN
    UPDATE users
       SET role = 'admin'
     WHERE id = (
       SELECT id FROM users ORDER BY created_at ASC, id ASC LIMIT 1
     );
  END IF;
END$$;
