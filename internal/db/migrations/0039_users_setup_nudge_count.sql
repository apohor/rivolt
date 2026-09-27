-- 0039_users_setup_nudge_count.sql — widen the finish-setup nudge.
--
-- Rationale:
--
--   0035 sent one "finish connecting" email, and only to users who had
--   completed onboarding. An audit of the no-vehicle users (Sep 2026)
--   found that half of them never completed onboarding at all - they
--   closed the tab on the connect step - so they were never emailed,
--   and none of the users who did get the single email came back.
--
--   The sweep now targets every no-vehicle user regardless of
--   onboarding state, and sends up to two emails: the first a day
--   after signup, a follow-up about a week after the first.
--   setup_nudge_count tracks how many went out; setup_nudged_at keeps
--   meaning "when the latest one was sent" and spaces the follow-up.
--
--   Backfill: anyone already nudged under 0035 has had their first.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS setup_nudge_count SMALLINT NOT NULL DEFAULT 0;

UPDATE users SET setup_nudge_count = 1
WHERE setup_nudged_at IS NOT NULL AND setup_nudge_count = 0;

-- The 0035 index keyed on onboarding_completed, which the sweep no
-- longer filters on. Replace it with one over the still-eligible rows.
DROP INDEX IF EXISTS users_setup_nudge_idx;
CREATE INDEX IF NOT EXISTS users_setup_nudge_idx
    ON users (created_at)
    WHERE setup_nudge_count < 2;
