-- 0006_sessions.sql — server-side opaque sessions.
--
-- Rationale (ARCHITECTURE decision 4, ROADMAP Phase 1):
--
-- Before this migration the cookie carried a signed
-- `{user_id, expires_at}` blob. The HMAC is fine; the lack of a
-- server-side record is not:
--
--   - Revocation is impossible without invalidating every other
--     cookie too (rotating the pepper). \"Sign out all other
--     devices\" is unimplementable.
--   - There is no audit surface. Nothing records which browsers
--     a user has signed into from, no last_seen_at for hijack
--     detection, no user_agent / ip to render a device list.
--   - A stolen cookie is valid until its embedded expiry, even
--     if the user explicitly logs out on another device.
--
-- This migration adds the source-of-truth table. The cookie
-- becomes a random 32-byte opaque token; the DB stores its
-- HMAC-SHA256 hash (under the existing cookie pepper) so a DB
-- dump doesn't grant session access without also having the
-- pepper out of the host env.
--
-- # Columns
--
--   - id             — surrogate primary key, stable identifier
--                      used by /api/auth/sessions (future) and
--                      by \"revoke this specific device\".
--   - user_id        — tenant this session belongs to. ON DELETE
--                      CASCADE cleans sessions up when a user is
--                      removed.
--   - token_hash     — HMAC(pepper, raw_token). Indexed, unique,
--                      used by the middleware lookup path.
--   - created_at     — when the session was minted. Useful for
--                      \"sessions older than 90 days\" reports.
--   - last_seen_at   — refreshed by middleware on every request,
--                      throttled to once per minute so a busy
--                      tab doesn't storm UPDATE traffic. Used
--                      for the device list UI (\"Active 2 min
--                      ago\") and for hijack heuristics (sudden
--                      jump in ip_address after long idle).
--   - expires_at     — absolute lifetime. Middleware treats
--                      rows past this as revoked.
--   - revoked_at     — set by explicit logout and by
--                      \"sign out all other devices\". Allows
--                      the row to stick around for a short
--                      post-mortem window even after revocation.
--   - user_agent     — raw UA string at login time. Persisted,
--                      not refreshed — the device label is the
--                      mutable surface.
--   - ip_address     — INET so a future v6-aware audit query
--                      works.
--   - device_label   — human-editable device name (\"work
--                      laptop\"). Populated by the future
--                      /api/auth/sessions PATCH surface.

CREATE TABLE IF NOT EXISTS sessions (
    id             UUID PRIMARY KEY,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash     BYTEA NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ,
    user_agent     TEXT,
    ip_address     INET,
    device_label   TEXT
);

-- Unique index on token_hash is the per-request hot path:
-- every authenticated call does one SELECT by this column.
-- UNIQUE is both a correctness guard (a collision would let
-- one session's cookie pick up another row) and the lookup
-- index.
CREATE UNIQUE INDEX IF NOT EXISTS sessions_token_hash_idx
    ON sessions (token_hash);

-- Per-user index backs the device-list UI and the
-- \"sign out all other devices\" mutation (DELETE WHERE
-- user_id = $1 AND id <> $keep).
CREATE INDEX IF NOT EXISTS sessions_user_id_idx
    ON sessions (user_id);

-- Expiry index is for the future janitor job that drops
-- rows past expires_at. Not a hot path; present so the
-- janitor doesn't seq-scan.
CREATE INDEX IF NOT EXISTS sessions_expires_at_idx
    ON sessions (expires_at);
