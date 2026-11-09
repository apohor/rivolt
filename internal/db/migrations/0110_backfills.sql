-- 0110_backfills.sql: progress of the background backfills (zero-impact deploys, G6b; internal/backfill).
--
-- A migration moves schema, not data: a table-wide UPDATE inside one holds row locks in one long transaction, bloats the table, and holds up the
-- migration job that runs before every rollout. Data that has to be moved after a schema change is moved by a named backfill job instead: keyset
-- batches in short transactions, run by one pod at a time, resumable. This table is where a job's place is kept, in the SAME transaction as the
-- batch it describes, so a crash or a drain between batches loses nothing and repeats nothing.
--   state    pending (not started), running, done, or failed (the last batch errored; retried after a pause)
--   cursor   the last key the job finished (opaque to this table; the job decides what it means)
-- A new table nobody else can see yet: no lock on anything that exists. Not per user, so no tenant policy.
CREATE TABLE IF NOT EXISTS backfills (
    name       TEXT        PRIMARY KEY,
    state      TEXT        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed')),
    cursor     TEXT        NOT NULL DEFAULT '',
    rows_done  BIGINT      NOT NULL DEFAULT 0,
    batch_size INTEGER     NOT NULL DEFAULT 500,
    error      TEXT        NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    done_at    TIMESTAMPTZ
);
