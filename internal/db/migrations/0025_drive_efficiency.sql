-- 0025_drive_efficiency.sql
--
-- Per-drive AI-generated efficiency analysis cache. The analysis
-- breaks down what drove a trip's mi/kWh -- weather, terrain, driving
-- style, climate control, payload, route, tire pressure -- with a
-- single concrete recommendation for the next drive.
--
-- Caching is structural: a finished drive is immutable, so the
-- analysis stays valid forever once generated. Without this table the
-- card shows an empty state again on every page reload (and every
-- pod rollout) because the SPA holds the result in component state
-- and loses it on remount. Re-running on every visit would also
-- re-bill the operator's LLM key.
--
-- Keyed (user_id, drive_id) where drive_id matches drives.external_id
-- within the user's scope -- external_id is unique per user (the
-- importer/recorder embeds the user-scoped session number into it),
-- so a composite PK is sufficient. We do not FK to drives because
-- (vehicle_id, external_id) is the upstream uniqueness pair and the
-- handler validates ownership before generating; a stale cache row
-- would be cleaned up by the user_id ON DELETE CASCADE.
--
-- result_json holds the structured JSON the model emitted (factors,
-- recommendation, forecast, summary). analysis_text duplicates the
-- raw reply for cheap text-only reads (debugging, log forwarding)
-- without having to parse the JSONB column.
--
-- RLS: not enabled on this table for the same reason 0020_drive_recaps
-- skipped it -- the app role currently bypasses RLS (see 0008's
-- "dormant suspenders" note). Phase 2 will enable RLS on every
-- user-scoped table at once and this row will be picked up there.

CREATE TABLE IF NOT EXISTS drive_efficiency (
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    drive_id       TEXT NOT NULL,
    model          TEXT NOT NULL,
    analysis_text  TEXT NOT NULL,
    result_json    JSONB NOT NULL,
    input_tokens   BIGINT NOT NULL DEFAULT 0,
    output_tokens  BIGINT NOT NULL DEFAULT 0,
    generated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, drive_id)
);
