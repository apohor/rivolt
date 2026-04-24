-- 0001_initial_schema.sql — Rivolt v0.4.x clean-slate schema.
--
-- Design principles:
--
--   1. Identity. Every row that belongs to a user has user_id UUID
--      NOT NULL and an FK with ON DELETE CASCADE. A user is keyed on
--      username; the UUID is a deterministic v5 hash of the
--      lower-cased username so the same account has the same id
--      across every Rivolt install — no central registry needed.
--
--   2. Vehicles are first-class. One user owns many vehicles. The
--      Rivian-side id moves to a column (rivian_vehicle_id);
--      everything downstream (charges, drives, samples) joins on the
--      internal UUID. Handing a vehicle to another family member is
--      one UPDATE.
--
--   3. Both inputs fit the same shape. ElectraFi CSV imports and the
--      live WebSocket recorder both write through charges / drives /
--      vehicle_state. `source` marks the provenance; `external_id`
--      preserves each writer's deterministic-id scheme so re-imports
--      and recorder restarts upsert cleanly.
--
--   4. Native types. TIMESTAMPTZ for time, NUMERIC for money,
--      DOUBLE PRECISION for telemetry floats. Anything squishy lives
--      in a JSONB `metadata` column so we can add "fast-charger
--      brand", "session temperature curve", etc. without a
--      migration.
--
--   5. Cascade everywhere. Deleting a user deletes their vehicles,
--      which deletes their charges, drives, samples, settings, and
--      push subscriptions. Self-service account deletion becomes
--      one statement.
--
--   6. Future-proofed. `locations` + nullable FKs from charges and
--      drives let Home / Work / favourite-supercharger live as real
--      rows once clustering graduates from per-request analytics.
--      `notes` + `tags` accept user annotations without bending
--      JSONB. `imports` tracks CSV runs for idempotency and the
--      Settings UI. `updated_at` indexes keep a future
--      /api/sync?since=… cursor query fast.

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- -------------------------------------------------------------------
-- users: identity root. Phase-1 seeds the single static-credential
-- operator; phase-3 will upsert one row per OIDC sub on first sign
-- in. UUID is deterministic from lower(username) — see db.UserIDFor.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id           UUID PRIMARY KEY,
    username     TEXT NOT NULL UNIQUE,
    email        TEXT,
    display_name TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- -------------------------------------------------------------------
-- vehicles: a car one user owns. rivian_vehicle_id is the
-- Rivian-gateway string id (e.g. "01-242521064") for live ingest;
-- importers that don't have a real id generate a stable synthetic
-- one (`electrafi_<hash>`) so the same UNIQUE(user_id,
-- rivian_vehicle_id) slot catches re-imports.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS vehicles (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rivian_vehicle_id  TEXT NOT NULL,
    vin                TEXT,
    display_name       TEXT,
    model              TEXT,
    trim               TEXT,
    model_year         INTEGER,
    pack_kwh           DOUBLE PRECISION,
    image_url          TEXT,
    notes              TEXT,
    tags               TEXT[] NOT NULL DEFAULT '{}',
    metadata           JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (user_id, rivian_vehicle_id)
);
CREATE INDEX IF NOT EXISTS vehicles_user_id    ON vehicles (user_id);
CREATE INDEX IF NOT EXISTS vehicles_updated_at ON vehicles (user_id, updated_at DESC);

-- -------------------------------------------------------------------
-- locations: user-named points of interest. Populated two ways —
-- the clustering pipeline promotes a recurring charge centroid to
-- "Home"/"Work" after N sessions, and the user can also name a
-- location explicitly from the UI (e.g. "Parents' place"). Nothing
-- references it mandatorily; charges.location_id and drives.*_id
-- are nullable FKs that stay NULL for one-off stops.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS locations (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL DEFAULT 'custom',      -- 'home' | 'work' | 'public' | 'custom'
    lat          DOUBLE PRECISION NOT NULL,
    lon          DOUBLE PRECISION NOT NULL,
    radius_m     DOUBLE PRECISION NOT NULL DEFAULT 200,
    price_per_kwh NUMERIC(12, 6),                     -- overrides global rate when a charge lands here
    currency     TEXT,
    metadata     JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS locations_user_id ON locations (user_id);
CREATE INDEX IF NOT EXISTS locations_user_kind ON locations (user_id, kind);

-- -------------------------------------------------------------------
-- charges: one row per charging session. Live recorder and ElectraFi
-- import both write here. `external_id` preserves each writer's
-- dedupe key:
--   live:    live_<rivian_vehicle_id>_c_<unix_started>
--   import:  electrafi_<vid>_c_<unix_started>
-- UNIQUE(vehicle_id, external_id) makes re-imports and recorder
-- reconnects a clean upsert.
--
-- session_type is "ac" / "dc" / NULL so the /charges filter can
-- drop a JOIN to samples — the recorder knows at close time, the
-- importer infers from peak power.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS charges (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id        UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    location_id       UUID REFERENCES locations(id) ON DELETE SET NULL,
    external_id       TEXT NOT NULL,
    started_at        TIMESTAMPTZ NOT NULL,
    ended_at          TIMESTAMPTZ NOT NULL,
    start_soc_pct     DOUBLE PRECISION,
    end_soc_pct       DOUBLE PRECISION,
    energy_added_kwh  DOUBLE PRECISION,
    miles_added       DOUBLE PRECISION,
    max_power_kw      DOUBLE PRECISION,
    avg_power_kw      DOUBLE PRECISION,
    session_type      TEXT,                      -- 'ac' | 'dc' | NULL
    final_state       TEXT,
    lat               DOUBLE PRECISION,
    lon               DOUBLE PRECISION,
    source            TEXT NOT NULL,             -- 'live' | 'electrafi_import' | …
    cost              NUMERIC(12, 4),
    currency          TEXT,
    price_per_kwh     NUMERIC(12, 6),
    notes             TEXT,
    tags              TEXT[] NOT NULL DEFAULT '{}',
    metadata          JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (vehicle_id, external_id)
);
CREATE INDEX IF NOT EXISTS charges_user_started      ON charges (user_id, started_at DESC);
CREATE INDEX IF NOT EXISTS charges_vehicle_started   ON charges (vehicle_id, started_at DESC);
CREATE INDEX IF NOT EXISTS charges_user_updated      ON charges (user_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS charges_location          ON charges (location_id) WHERE location_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS charges_user_open_live    ON charges (user_id, vehicle_id, final_state)
    WHERE source = 'live'
      AND final_state LIKE 'charging\_%'
      AND final_state NOT IN ('charging_complete', 'charging_station_err');

-- -------------------------------------------------------------------
-- drives: one row per drive session.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS drives (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id         UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    start_location_id  UUID REFERENCES locations(id) ON DELETE SET NULL,
    end_location_id    UUID REFERENCES locations(id) ON DELETE SET NULL,
    external_id        TEXT NOT NULL,
    started_at         TIMESTAMPTZ NOT NULL,
    ended_at           TIMESTAMPTZ NOT NULL,
    start_soc_pct      DOUBLE PRECISION,
    end_soc_pct        DOUBLE PRECISION,
    start_odometer_mi  DOUBLE PRECISION,
    end_odometer_mi    DOUBLE PRECISION,
    distance_mi        DOUBLE PRECISION,
    start_lat          DOUBLE PRECISION,
    start_lon          DOUBLE PRECISION,
    end_lat            DOUBLE PRECISION,
    end_lon            DOUBLE PRECISION,
    max_speed_mph      DOUBLE PRECISION,
    avg_speed_mph      DOUBLE PRECISION,
    source             TEXT NOT NULL,
    notes              TEXT,
    tags               TEXT[] NOT NULL DEFAULT '{}',
    metadata           JSONB NOT NULL DEFAULT '{}'::JSONB,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (vehicle_id, external_id)
);
CREATE INDEX IF NOT EXISTS drives_user_started     ON drives (user_id, started_at DESC);
CREATE INDEX IF NOT EXISTS drives_vehicle_started  ON drives (vehicle_id, started_at DESC);
CREATE INDEX IF NOT EXISTS drives_user_updated     ON drives (user_id, updated_at DESC);

-- -------------------------------------------------------------------
-- vehicle_state: raw polling snapshots. PK is (vehicle_id, at) —
-- the natural dedupe key for both the live WebSocket frame stream
-- and the ElectraFi row stream.
--
-- user_id is denormalized on the row so queries filter without a
-- JOIN to vehicles. `raw JSONB` is an optional archive of the full
-- payload — the live recorder can stash the unparsed frame there so
-- we can re-derive samples when column sets change, without losing
-- the original.
--
-- This is the one table that will genuinely grow. A partitioning
-- migration (BY RANGE (at), monthly) or a TimescaleDB hypertable
-- can be layered on later without touching readers/writers.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS vehicle_state (
    vehicle_id        UUID NOT NULL REFERENCES vehicles(id) ON DELETE CASCADE,
    at                TIMESTAMPTZ NOT NULL,
    user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    battery_level_pct DOUBLE PRECISION,
    range_mi          DOUBLE PRECISION,
    odometer_mi       DOUBLE PRECISION,
    lat               DOUBLE PRECISION,
    lon               DOUBLE PRECISION,
    speed_mph         DOUBLE PRECISION,
    shift_state       TEXT,
    charging_state    TEXT,
    charger_power_kw  DOUBLE PRECISION,
    charge_limit_pct  DOUBLE PRECISION,
    inside_temp_c     DOUBLE PRECISION,
    outside_temp_c    DOUBLE PRECISION,
    drive_number      BIGINT,
    charge_number     BIGINT,
    source            TEXT NOT NULL,
    raw               JSONB,
    PRIMARY KEY (vehicle_id, at)
);
CREATE INDEX IF NOT EXISTS vehicle_state_user_at ON vehicle_state (user_id, at DESC);

-- -------------------------------------------------------------------
-- imports: one row per ElectraFi upload (or any future importer).
-- The Settings UI lists these so the user can see what's been
-- ingested; checksum + filename let us short-circuit a duplicate
-- upload long before we start parsing.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS imports (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vehicle_id   UUID REFERENCES vehicles(id) ON DELETE SET NULL,
    source       TEXT NOT NULL,                   -- 'electrafi' | 'teslafi' | …
    filename     TEXT NOT NULL,
    sha256       TEXT,
    bytes        BIGINT,
    rows         INTEGER NOT NULL DEFAULT 0,
    samples      INTEGER NOT NULL DEFAULT 0,
    drives       INTEGER NOT NULL DEFAULT 0,
    charges      INTEGER NOT NULL DEFAULT 0,
    skipped      INTEGER NOT NULL DEFAULT 0,
    started_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at  TIMESTAMPTZ,
    error        TEXT,
    metadata     JSONB NOT NULL DEFAULT '{}'::JSONB
);
CREATE INDEX IF NOT EXISTS imports_user_started ON imports (user_id, started_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS imports_user_sha
    ON imports (user_id, source, sha256)
    WHERE sha256 IS NOT NULL;

-- -------------------------------------------------------------------
-- push_subscriptions: browser push subscriptions. Endpoint is
-- globally unique (it's the browser's own URL) but we compound-key
-- on user_id so one user can have many devices without a second
-- id column.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS push_subscriptions (
    user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    endpoint            TEXT NOT NULL,
    p256dh              TEXT NOT NULL,
    auth                TEXT NOT NULL,
    on_charging_done    BOOLEAN NOT NULL DEFAULT TRUE,
    on_plug_in_reminder BOOLEAN NOT NULL DEFAULT TRUE,
    on_anomaly          BOOLEAN NOT NULL DEFAULT TRUE,
    user_agent          TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, endpoint)
);

-- -------------------------------------------------------------------
-- push_vapid: one keypair per Rivolt install. Identifies the
-- *server*, not any user, so it stays a singleton table.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS push_vapid (
    id          SMALLINT PRIMARY KEY CHECK (id = 1),
    public_key  TEXT NOT NULL,
    private_key TEXT NOT NULL,
    subject     TEXT NOT NULL
);

-- -------------------------------------------------------------------
-- user_settings: typed-ish per-user settings, stored as JSONB so the
-- surface can grow (UI prefs, charging rates, AI provider configs,
-- Rivian session) without a migration per field.
--
-- Keying on (user_id, namespace) gives us grouped settings:
--   namespace='ai'        → AI provider keys + models
--   namespace='charging'  → default $/kWh, currency, home coords
--   namespace='rivian'    → persisted Rivian session (access + refresh tokens)
--   namespace='ui'        → sparkline toggles, default time window, etc.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS user_settings (
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    namespace  TEXT NOT NULL,
    value      JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, namespace)
);

-- -------------------------------------------------------------------
-- ai_usage: append-only LLM call ledger. Per-user so the billing
-- story is clean when we run shared deployments.
-- -------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS ai_usage (
    id            BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    occurred_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL,
    feature       TEXT NOT NULL,
    input_tokens  BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd      NUMERIC(12, 6) NOT NULL DEFAULT 0,
    duration_ms   BIGINT NOT NULL DEFAULT 0,
    ok            BOOLEAN NOT NULL DEFAULT TRUE,
    error         TEXT,
    metadata      JSONB NOT NULL DEFAULT '{}'::JSONB
);
CREATE INDEX IF NOT EXISTS ai_usage_user_time     ON ai_usage (user_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS ai_usage_user_provider ON ai_usage (user_id, provider);
CREATE INDEX IF NOT EXISTS ai_usage_user_feature  ON ai_usage (user_id, feature);

-- -------------------------------------------------------------------
-- updated_at auto-touch. Keeps callers from having to remember to
-- set it on every UPDATE, which matters for the future
-- /api/sync?since=… mobile cursor.
-- -------------------------------------------------------------------
CREATE OR REPLACE FUNCTION rivolt_touch_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at := NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER users_touch_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
CREATE TRIGGER vehicles_touch_updated_at
    BEFORE UPDATE ON vehicles
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
CREATE TRIGGER locations_touch_updated_at
    BEFORE UPDATE ON locations
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
CREATE TRIGGER charges_touch_updated_at
    BEFORE UPDATE ON charges
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
CREATE TRIGGER drives_touch_updated_at
    BEFORE UPDATE ON drives
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
CREATE TRIGGER push_subscriptions_touch_updated_at
    BEFORE UPDATE ON push_subscriptions
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
CREATE TRIGGER user_settings_touch_updated_at
    BEFORE UPDATE ON user_settings
    FOR EACH ROW EXECUTE FUNCTION rivolt_touch_updated_at();
