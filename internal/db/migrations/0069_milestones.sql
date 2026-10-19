-- 0069_milestones.sql — the milestone cards and their once-ever guard.
--
--   milestones            one row per (account, vehicle, key) the first time a
--                         milestone is passed: miles_1000, home_mwh_1, charges_100,
--                         lease_half, and best_eff:YYYY-MM (at most one a month).
--                         status 'achieved' shows as a card (dismissible, 30 days);
--                         'backfilled' marks a milestone already passed when the
--                         feature shipped: kept only so it is never announced, never
--                         shown. detail holds aggregates (numbers) for the card text,
--                         never a place, a coordinate or a time of day. Rows are not
--                         purged (a purged row would let the milestone fire again);
--                         they go with the account (CASCADE).
--   milestones_backfill   a single row, written once the existing milestones have
--                         been marked backfilled; until then no milestone is
--                         announced.
--
-- RLS: the 0059 pattern (stage 1, ENABLE only) on milestones.
-- Rollback: DROP TABLE milestones, milestones_backfill; nothing else reads them.

CREATE TABLE IF NOT EXISTS milestones (
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id   UUID        NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    key          TEXT        NOT NULL CHECK (key ~ '^[a-z0-9_:-]{1,40}$'),
    status       TEXT        NOT NULL CHECK (status IN ('achieved', 'backfilled')),
    achieved_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    detail       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    dismissed_at TIMESTAMPTZ,
    PRIMARY KEY (user_id, vehicle_id, key)
);
CREATE INDEX IF NOT EXISTS milestones_user_achieved ON milestones (user_id, achieved_at DESC) WHERE status = 'achieved';

CREATE TABLE IF NOT EXISTS milestones_backfill (
    id      SMALLINT    PRIMARY KEY CHECK (id = 1),
    done_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

ALTER TABLE milestones ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS milestones_tenant_isolation ON milestones;
CREATE POLICY milestones_tenant_isolation ON milestones
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
