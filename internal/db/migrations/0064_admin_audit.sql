-- 0064_admin_audit.sql — a record of every admin write (who, what, on whom,
-- when, from which address), kept two years.
--
-- A row is inserted BEFORE the write runs and finished with its outcome after,
-- so no admin write can happen without a row (if the insert fails the write is
-- refused); a row whose outcome stays NULL is a write whose result is unknown
-- (the process died mid-request). `details` holds only booleans, numbers and
-- short enum tokens (never free text, secrets or emails). `ip` is the real
-- client address (the admin's own). There are no foreign keys on purpose: the
-- record must outlive the accounts it names. Read-only admin views are not
-- logged; impersonation start and stop are.
CREATE TABLE IF NOT EXISTS admin_audit (
    id             BIGSERIAL   PRIMARY KEY,
    at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor_user_id  UUID        NOT NULL,
    action         TEXT        NOT NULL,
    target_type    TEXT        NOT NULL DEFAULT '',
    target_id      TEXT        NOT NULL DEFAULT '',
    target_user_id UUID,
    details        JSONB       NOT NULL DEFAULT '{}'::jsonb,
    ip             TEXT        NOT NULL DEFAULT '',
    outcome        TEXT        CHECK (outcome IN ('ok', 'failed')),
    status         INTEGER,
    finished_at    TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS admin_audit_at ON admin_audit (at DESC);
CREATE INDEX IF NOT EXISTS admin_audit_actor ON admin_audit (actor_user_id, id DESC);
CREATE INDEX IF NOT EXISTS admin_audit_target_user ON admin_audit (target_user_id, id DESC) WHERE target_user_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS admin_audit_action ON admin_audit (action, id DESC);
