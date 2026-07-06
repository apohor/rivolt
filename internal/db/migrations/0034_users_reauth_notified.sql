-- 0034_users_reauth_notified.sql — track when a stuck-session nudge
-- was last emailed, so the re-nudge sweep doesn't spam.
--
-- Rationale:
--
--   The re-auth email (see cmd/rivolt sendReauthEmail) fires once,
--   on the rising edge of needs_reauth. Two gaps left users stranded:
--
--     1. Users who flipped needs_reauth BEFORE the email feature
--        shipped never got any notification at all — the rising edge
--        had already passed. (Real case: a user stuck since early
--        June, never told.)
--     2. A single email is easy to miss. Nothing ever followed up.
--
--   A periodic sweep now re-nudges anyone still stuck, throttled by
--   this column: send only when it's NULL (never notified — covers
--   the pre-feature backlog) or older than the cooldown.
--
--   NULL default (no backfill) is deliberate: every currently-stuck
--   user reads as "never notified" and is picked up on the first
--   sweep, which is exactly the recovery we want.
--
--   Cleared alongside needs_reauth on a successful Login so the next
--   lock-out episode starts a fresh notification clock.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS needs_reauth_notified_at TIMESTAMPTZ;

-- Partial index over just the flagged rows keeps the sweep's "who is
-- due?" scan proportional to the (small) stuck set, not the whole
-- users table.
CREATE INDEX IF NOT EXISTS users_needs_reauth_idx
    ON users (needs_reauth_notified_at)
    WHERE needs_reauth;
