-- 0047_trip_plan_links.sql — Plan vs Actual links (B12, contract
-- scratchpad/plan-vs-actual-contract.md §3).
--
-- A saved trip (a plan) can be linked, by the user, to the outing that
-- actually happened. The row snapshots the plan's inputs/plan JSON at
-- link time so re-planning the saved trip later never rewrites the
-- comparison. An 'unlinked' row records "this outing is not that
-- plan" so it is never suggested again. At most one 'linked' row per
-- saved trip (the partial unique index); linking another outing turns
-- the previous row 'unlinked'.
--
-- trip_id is the ledger's outing id ('trip_<first drive id>').
-- started_at/ended_at re-find the outing if a re-import or dedupe
-- changed its first drive (link_stale).

CREATE TABLE IF NOT EXISTS trip_plan_links (
    saved_trip_id UUID        NOT NULL REFERENCES saved_trips(id) ON DELETE CASCADE,
    user_id       UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    trip_id       TEXT        NOT NULL,
    status        TEXT        NOT NULL CHECK (status IN ('linked', 'unlinked')),
    confidence    NUMERIC(3,2),
    started_at    TIMESTAMPTZ NOT NULL,
    ended_at      TIMESTAMPTZ NOT NULL,
    plan_snapshot JSONB       NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (saved_trip_id, trip_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS trip_plan_links_one_linked
    ON trip_plan_links (saved_trip_id) WHERE status = 'linked';

CREATE INDEX IF NOT EXISTS trip_plan_links_user ON trip_plan_links (user_id);
