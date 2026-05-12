-- signup_requests: pre-account waitlist entries created by anyone
-- visiting /signup without an invite code in hand. An admin reviews
-- pending rows and either approves (which mints a fresh invite_code
-- and emails it to the requester) or rejects.
--
-- Not a user-scoped table: rows exist before any user account does,
-- so no user_id column and no RLS. The decided_by column points at
-- the admin who acted on the row.
CREATE TABLE IF NOT EXISTS signup_requests (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT        NOT NULL,
    message       TEXT        NOT NULL DEFAULT '',
    status        TEXT        NOT NULL DEFAULT 'pending'
                              CHECK (status IN ('pending','approved','rejected')),
    invite_code   TEXT        REFERENCES invite_codes(code) ON DELETE SET NULL,
    decided_by    UUID        REFERENCES users(id) ON DELETE SET NULL,
    decided_at    TIMESTAMPTZ,
    requested_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Only one pending request per email at a time. Re-requests after
-- a reject or approve are allowed (status changes the uniqueness key
-- via the partial index).
CREATE UNIQUE INDEX IF NOT EXISTS signup_requests_pending_email
    ON signup_requests (email)
    WHERE status = 'pending';

-- Admin list view orders by recency.
CREATE INDEX IF NOT EXISTS signup_requests_status_requested_at
    ON signup_requests (status, requested_at DESC);
