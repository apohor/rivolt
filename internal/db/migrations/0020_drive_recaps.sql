-- 0020_drive_recaps.sql
--
-- Per-drive AI-generated trip recap cache. The recap is a 2-3
-- sentence narration ("65 mi from Austin to San Antonio, 2.4
-- mi/kWh, +280 ft net climb, charged once at the Buc-ee's DC
-- fast for 22 min, total $4.40") produced on demand from the
-- drive's aggregates and per-sample telemetry.
--
-- Caching is structural: a finished drive is immutable, so the
-- recap stays valid forever once generated. Without this table
-- every page navigation would re-bill the operator's LLM key.
--
-- Keyed (user_id, drive_id) where drive_id matches drives.external_id
-- within the user's scope -- external_id is unique per user (the
-- importer/recorder embeds the user-scoped session number into it),
-- so a composite PK is sufficient. We do not FK to drives because
-- (vehicle_id, external_id) is the upstream uniqueness pair and the
-- handler validates ownership before generating; a stale cache row
-- would be cleaned up by the user_id ON DELETE CASCADE.

CREATE TABLE IF NOT EXISTS drive_recaps (
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    drive_id       TEXT NOT NULL,
    model          TEXT NOT NULL,
    recap          TEXT NOT NULL,
    input_tokens   BIGINT NOT NULL DEFAULT 0,
    output_tokens  BIGINT NOT NULL DEFAULT 0,
    generated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, drive_id)
);
