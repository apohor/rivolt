-- 0042_idp_delete_failures.sql — remember identity-provider deletions
-- that failed, so an admin can see and retry them.
--
-- Rationale:
--
--   Deleting an account removes the rivolt rows first (one transaction)
--   and then the IdP identity (Kratos). If the IdP call fails, the
--   person can still sign in to an account that no longer exists in
--   Rivolt, and until now the only trace was a WARN log line. Each
--   failure is recorded here; the admin page lists the pending ones and
--   retries them. A row is deleted once the IdP deletion succeeds, so
--   the username (an email address) isn't kept longer than needed.

CREATE TABLE IF NOT EXISTS idp_delete_failures (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    username   TEXT        NOT NULL,
    error      TEXT        NOT NULL,
    attempts   INTEGER     NOT NULL DEFAULT 1,
    failed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_tried TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idp_delete_failures_username
    ON idp_delete_failures (lower(username));
