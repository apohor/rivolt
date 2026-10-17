-- 0056_inactive_account_purge.sql — accounts that never connected a vehicle
-- are warned at day 90 and deleted at day 120 (owner rule; behind the
-- inactive_account_purge flag, off by default).
--
--   last_login_at           newest session creation for the user: the
--                           "signed in" clock. Signing in resets the 90/120
--                           day clock and cancels a pending warning. Set by
--                           sessions.Store.Create; backfilled below from the
--                           newest existing session.
--   inactive_warned_at      when the "we'll delete your account on <date>"
--                           email was SENT (recorded after a successful send,
--                           so a failed send is retried and never counts as a
--                           warning). NULL = not warned.
--   inactive_warn_claimed_at  a one-hour lease taken before sending, so two
--                           replicas don't both send; an expired lease with
--                           no sent warning is retried.
--
-- The clock is GREATEST(created_at, last_login_at). Metadata-only ADD
-- COLUMNs; the backfill touches the (small) users table once.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS last_login_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS inactive_warned_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS inactive_warn_claimed_at TIMESTAMPTZ;

UPDATE users u
   SET last_login_at = s.newest
  FROM (SELECT user_id, max(created_at) AS newest FROM sessions GROUP BY user_id) s
 WHERE s.user_id = u.id AND u.last_login_at IS NULL;
