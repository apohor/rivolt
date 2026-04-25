-- 0003_flags.sql — global operational flags.
--
-- Rationale (ARCHITECTURE.md decision 6, ROADMAP Phase 1):
--
--   1. Kill switch. When Rivian's gateway starts rate-limiting us,
--      when we detect an incident, or when we deliberately want to
--      pause the service without a deploy, the operator flips a
--      single boolean. Every Rivian-facing code path polls this
--      row every ~10 seconds and refuses to dial upstream when it
--      reads true. Togglable over psql, HTTP, or in a DB console
--      — no build, no rollout, no pod restart.
--
--   2. Global scope. This is explicitly NOT per-user. A per-user
--      flag belongs on the users row (future: users.needs_reauth
--      for the error-classification work). `flags` is for
--      operator-level circuit breakers that apply to the whole
--      server.
--
--   3. Singleton row pattern. One row, pk='global'. We could have
--      used individual boolean columns on a settings-ish table,
--      but an enum-keyed row lets us add new flags (e.g.
--      pause_digest, pause_push) without a migration per switch.
--      `value` is JSONB so flags can carry metadata (set_by,
--      reason) without the pain of schema churn.

CREATE TABLE IF NOT EXISTS flags (
    name       TEXT PRIMARY KEY,
    value      JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT
);

-- Seed the kill switch in the OFF position. Using ON CONFLICT DO
-- NOTHING so re-running the migration (which shouldn't happen,
-- but migrations are idempotent by design) can't clobber an
-- operator's in-flight kill decision.
INSERT INTO flags (name, value, updated_by)
VALUES ('rivian_upstream_paused', '{"paused": false}'::jsonb, 'migration:0003')
ON CONFLICT (name) DO NOTHING;
