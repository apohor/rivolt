-- 0067_engagement.sql — the shared layer for non-transactional messages
-- (plug-in reminder, weekly recap, milestones): a send log that is also the
-- idempotency key, per-account prefs, and the milestones push toggle.
--
--   engagement_sends  one row per (account, kind, key), claimed with INSERT …
--                     ON CONFLICT DO NOTHING before anything is sent, so each
--                     message fires at most once across replicas and restarts.
--                     status: sending | sent | failed | skipped_empty |
--                     skipped_backfill. budgeted=false marks a user-scheduled
--                     utility (the plug-in reminder), which neither counts
--                     toward nor is stopped by the 4-per-30-days budget.
--                     Rows older than 400 days are purged by the retention job.
--   engagement_prefs  timezone (NULL until the user's browser stored one; the
--                     plug-in reminder never sends without it), weekly_recap
--                     (opt-in, default off), plug_threshold_pct (default 40) and
--                     plug_remind_at (default 20:00, never after 22:00).
--   push_subscriptions.on_milestones      new, default FALSE for everyone.
--   push_subscriptions.on_plug_in_reminder default is now FALSE and every
--                     existing row is set FALSE: the column defaulted to TRUE
--                     before anything could send it, so no account chose it.
--                     The reminder is opt-in only.
--
-- Rollback: DROP TABLE engagement_sends, engagement_prefs;
--   ALTER TABLE push_subscriptions DROP COLUMN on_milestones;
--   ALTER TABLE push_subscriptions ALTER COLUMN on_plug_in_reminder SET DEFAULT TRUE;
-- (the old per-row on_plug_in_reminder values are not recoverable, and no code
-- read them before this migration).

CREATE TABLE IF NOT EXISTS engagement_sends (
    user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       TEXT        NOT NULL CHECK (kind ~ '^[a-z0-9_]{1,40}$'),
    key        TEXT        NOT NULL CHECK (length(key) BETWEEN 1 AND 100),
    channel    TEXT        NOT NULL CHECK (channel IN ('push', 'email')),
    status     TEXT        NOT NULL CHECK (status IN ('sending', 'sent', 'failed', 'skipped_empty', 'skipped_backfill')),
    budgeted   BOOLEAN     NOT NULL DEFAULT TRUE,
    claimed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    sent_at    TIMESTAMPTZ,
    PRIMARY KEY (user_id, kind, key)
);
CREATE INDEX IF NOT EXISTS engagement_sends_claimed ON engagement_sends (claimed_at);
CREATE INDEX IF NOT EXISTS engagement_sends_user_claimed ON engagement_sends (user_id, claimed_at) WHERE budgeted;

CREATE TABLE IF NOT EXISTS engagement_prefs (
    user_id           UUID        PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    timezone          TEXT,
    weekly_recap      BOOLEAN     NOT NULL DEFAULT FALSE,
    plug_threshold_pct INTEGER    NOT NULL DEFAULT 40 CHECK (plug_threshold_pct BETWEEN 5 AND 95),
    plug_remind_at    TIME        NOT NULL DEFAULT '20:00' CHECK (plug_remind_at <= TIME '22:00'),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Tenant row-level security, the 0059 pattern (stage 1: ENABLE only).
DO $$
DECLARE
    t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['engagement_sends', 'engagement_prefs'] LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
        EXECUTE format($f$
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id())
        $f$, t || '_tenant_isolation', t);
    END LOOP;
END $$;

ALTER TABLE push_subscriptions ADD COLUMN IF NOT EXISTS on_milestones BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE push_subscriptions ALTER COLUMN on_plug_in_reminder SET DEFAULT FALSE;
UPDATE push_subscriptions SET on_plug_in_reminder = FALSE WHERE on_plug_in_reminder;
