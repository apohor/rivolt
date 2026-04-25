-- 0004_users_needs_reauth.sql — per-user Rivian re-auth gate.
--
-- Rationale (ARCHITECTURE decision 8 + ROADMAP Phase 1):
--
--   When Rivian's gateway returns a user-action error class
--   (credentials rejected, MFA required mid-session, account
--   suspended, etc.), retrying is actively harmful — it looks
--   like automated brute-force and can escalate the block.
--   Rivolt's response is to raise a flag on the affected user,
--   suppress every subsequent outbound call for that user until
--   they log in again, and surface the "please re-authenticate"
--   state to the UI.
--
--   Scope is per-user on purpose. The global kill switch
--   (flags.rivian_upstream_paused) is for operator-level
--   circuit-breaking; needs_reauth is for a single compromised
--   session. A rate-limit answer against one account doesn't
--   mean we stop serving every other tenant.
--
-- Columns:
--
--   - needs_reauth        : the gate bit. Checked on every
--                           outbound Rivian call for this user.
--   - needs_reauth_reason : free-text hint stored for the UI and
--                           for support debugging. Comes from
--                           the classifier — "password rejected",
--                           "MFA required", "account locked", ...
--   - needs_reauth_at     : when the flag was last raised. Lets
--                           the UI say "login expired 3 hours
--                           ago"; also useful for
--                           stuck-flag cleanup queries.
--
-- A successful Login clears all three back to NULL/FALSE.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS needs_reauth        BOOLEAN     NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS needs_reauth_reason TEXT,
    ADD COLUMN IF NOT EXISTS needs_reauth_at     TIMESTAMPTZ;
