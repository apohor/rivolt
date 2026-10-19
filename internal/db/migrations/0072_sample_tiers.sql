-- 0072_sample_tiers.sql: one row per monthly vehicle_state partition that the
-- sample thinning job (internal/samples/thin.go) has reduced: how many rows and
-- bytes it held before and after, when it was thinned, and when the full copy
-- kept for undo was dropped (7 days later). Fleet-level bookkeeping with no
-- user columns, so no RLS; never purged (a few rows a year).
--
-- Rollback: DROP TABLE sample_tiers. (A thinned partition stays thinned; a
-- *_full table still detached can be swapped back by hand, see docs/ARCHITECTURE.md.)
CREATE TABLE IF NOT EXISTS sample_tiers (
    month           DATE        PRIMARY KEY,  -- the partition's first day (UTC)
    rows_before     BIGINT      NOT NULL,
    rows_after      BIGINT      NOT NULL,
    bytes_before    BIGINT      NOT NULL,
    bytes_after     BIGINT      NOT NULL,
    thinned_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    full_dropped_at TIMESTAMPTZ
);
