-- 0048_statement_email.sql: monthly statement email (PM spec
-- scratchpad/statement-email-spec.md). Off by default twice: the
-- operator flag statement_email_enabled AND the user's own opt-in here.
--
-- statement_email_prefs: one row per user who ever touched the toggle.
--   enabled   the user's opt-in (default false).
--   timezone  IANA name, validated by the API; the SPA fills it from
--             the browser at the moment of opt-in; UTC when absent.
--
-- statement_email_sends: the idempotency key. The job inserts
-- (user_id, period) with status 'sending' BEFORE it sends, so two
-- replicas (or two runs) can never send the same month twice.
--   status    sending | sent | failed | skipped
--   attempts  send attempts; failed rows are retried for 3 days.
-- Rows older than 13 months are deleted by the job.

CREATE TABLE IF NOT EXISTS statement_email_prefs (
    user_id    UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled    BOOLEAN     NOT NULL DEFAULT FALSE,
    timezone   TEXT        NOT NULL DEFAULT 'UTC',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS statement_email_sends (
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    period     TEXT        NOT NULL CHECK (period ~ '^[0-9]{4}-[0-9]{2}$'),
    status     TEXT        NOT NULL CHECK (status IN ('sending', 'sent', 'failed', 'skipped')),
    attempts   INTEGER     NOT NULL DEFAULT 0,
    reason     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at    TIMESTAMPTZ,
    PRIMARY KEY (user_id, period)
);

CREATE INDEX IF NOT EXISTS statement_email_sends_created ON statement_email_sends (created_at);
