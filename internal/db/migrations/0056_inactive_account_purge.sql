-- 0056_inactive_account_purge.sql — accounts that never connected a vehicle
-- are warned at day 90 and deleted at day 120 after signup (owner rule;
-- behind the inactive_account_purge flag, off by default). Only connecting a
-- vehicle cancels it; signing in does not.
--
--   inactive_warned_at      when the "we'll delete your account on <date>"
--                           email was SENT (recorded after a successful send,
--                           so a failed send is retried and never counts as a
--                           warning). NULL = not warned.
--   inactive_warn_claimed_at  a one-hour lease taken before sending, so two
--                           replicas don't both send; an expired lease with
--                           no sent warning is retried.
--
-- Metadata-only ADD COLUMNs.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS inactive_warned_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS inactive_warn_claimed_at TIMESTAMPTZ;
