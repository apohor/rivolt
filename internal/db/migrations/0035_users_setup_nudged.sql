-- 0035_users_setup_nudged.sql — one-time "finish connecting" nudge.
--
-- Rationale:
--
--   Most signups that complete onboarding but never connect a Rivian
--   account simply go quiet - they hit the credential step, bounce,
--   and never come back. The app records telemetry for connected
--   users 24/7 but has no loop that reaches back to the ones who
--   stalled at the doorway.
--
--   This column marks that the finish-setup email has been sent so
--   the sweep fires exactly once per user. NULL = not yet nudged
--   (every existing stalled user is eligible on the first sweep).
--
--   No "clear on connect" is needed: once the user connects they have
--   a vehicles row, which drops them from the sweep's candidate query
--   regardless of this column. The column only prevents re-sending to
--   someone who was nudged and still hasn't connected.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS setup_nudged_at TIMESTAMPTZ;

-- Partial index over the eligible-but-unnudged rows keeps the sweep's
-- candidate scan cheap.
CREATE INDEX IF NOT EXISTS users_setup_nudge_idx
    ON users (created_at)
    WHERE onboarding_completed AND setup_nudged_at IS NULL;
