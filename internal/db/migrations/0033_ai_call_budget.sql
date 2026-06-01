-- 0033_ai_call_budget.sql — per-user daily cap on LLM-backed endpoints.
--
-- Two pieces:
--
--   1. An operator-controlled cap, stored as a flags row (see
--      0003_flags.sql) so the existing 10s poll + admin-write
--      plumbing covers it without a parallel config table.
--      value JSON: {"daily_limit": 50}. Unlike signup_cap this
--      fails OPEN: daily_limit <= 0 means "no cap" so a missing
--      or malformed row never bricks the AI features — the gate
--      is a cost backstop, not a security control.
--
--   2. A per-user daily counter the gate consumes before each
--      LLM call (/api/trips/plan/advice, /api/drives/{id}/efficiency).
--      One row per (user_id, day); the gate increments it atomically
--      and refuses once it would exceed the cap. Old rows are
--      harmless — a nightly/periodic prune is optional, not required
--      for correctness.

INSERT INTO flags (name, value, updated_by)
VALUES ('ai_call_cap', '{"daily_limit": 50}'::jsonb, 'migration:0033')
ON CONFLICT (name) DO NOTHING;

CREATE TABLE IF NOT EXISTS ai_call_usage (
    user_id  UUID  NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day      DATE  NOT NULL,
    calls    INT   NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, day)
);

-- Row-level security, mirroring 0008. Dormant-but-ready: the app
-- still connects as the DB owner today, so the app-level user_id
-- predicate remains the live filter; the policy activates with the
-- future role split with zero churn here.
ALTER TABLE ai_call_usage ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS ai_call_usage_tenant_isolation ON ai_call_usage;
CREATE POLICY ai_call_usage_tenant_isolation ON ai_call_usage
    USING (user_id = rivolt_current_user_id())
    WITH CHECK (user_id = rivolt_current_user_id());
