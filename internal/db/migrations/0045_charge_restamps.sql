-- 0045_charge_restamps.sql: make a home-pin correction (A15 re-stamp)
-- reversible.
--
-- Rationale:
--
--   POST /api/charges/restamp-place recomputes the recorded place on past
--   charges after the user says their old home pin was wrong, and (A15b)
--   prices the AC charges that become home and were unpriced at the home
--   rate. That overwrites the place snapshot that protects history after
--   a move, so each correction is logged here (one restamp_id per
--   correction, one row per changed charge) with what it replaced, and
--   POST /api/charges/restamp-place/undo puts it back. stamped_cost is
--   the price the correction added (NULL if none), so undo only removes a
--   price the user hasn't changed since.

CREATE TABLE IF NOT EXISTS charge_restamps (
    restamp_id   UUID        NOT NULL,
    user_id      UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    charge_id    UUID        NOT NULL REFERENCES charges(id) ON DELETE CASCADE,
    prev_place   TEXT,
    stamped_cost NUMERIC(12,4),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    undone_at    TIMESTAMPTZ,
    PRIMARY KEY (restamp_id, charge_id)
);

CREATE INDEX IF NOT EXISTS charge_restamps_user_created
    ON charge_restamps (user_id, created_at DESC);
