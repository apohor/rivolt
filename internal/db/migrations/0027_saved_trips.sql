-- 0027_saved_trips.sql
--
-- Per-user named trip templates for the planner. Each row holds the
-- inputs the user typed in (origin, destination, via stops, target SoC,
-- drive mode, adapter, departure preset) plus a snapshot of the last
-- computed plan + AI advice. The snapshot lets the saved-trips list
-- render the map + waypoint table instantly on click without a fresh
-- Rivian round-trip; the frontend prompts a re-plan when the snapshot
-- is older than a few hours since station availability, weather, and
-- ETA all drift.
--
-- inputs/plan/advice are JSONB so the schema stays decoupled from the
-- frontend shape. plan and advice are nullable for the "saved before
-- ever planning" case (the UI doesn't currently expose that, but the
-- schema shouldn't force ordering).
--
-- (user_id, name) is unique so the UI can offer "save with this name
-- replaces the existing entry" without an explicit id round-trip.
CREATE TABLE IF NOT EXISTS saved_trips (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT        NOT NULL,
    inputs      JSONB       NOT NULL,
    plan        JSONB,
    advice      JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, name)
);

CREATE INDEX IF NOT EXISTS saved_trips_user_updated
    ON saved_trips (user_id, updated_at DESC);
