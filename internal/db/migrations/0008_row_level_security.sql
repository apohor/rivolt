-- 0008_row_level_security.sql
--
-- Installs row-level-security policies on every user-scoped table.
-- Closes Phase-1 roadmap item "Row-level security on every
-- user-scoped table" with a staged rollout:
--
--   phase 1 (this migration): ENABLE ROW LEVEL SECURITY.
--     Policies exist and are enforced for any role that isn't the
--     table owner / not BYPASSRLS. The app still connects as the
--     DB owner today, so the effective behaviour is unchanged —
--     app-level `user_id` predicates remain the sole filter. The
--     policies are dormant-but-ready suspenders.
--
--   phase 2 (future): split the app role, drop BYPASSRLS, set
--     `app.user_id` per connection. Policies activate with zero
--     schema churn.
--
-- Why not FORCE ROW LEVEL SECURITY now? Because the login flow
-- looks up `users` BEFORE it knows which user is authenticating,
-- and the sessions lookup in `Middleware` runs before we can set
-- `app.user_id`. Enforcing both against a dormant setting would
-- brick login on first deploy. Phase 2 ships alongside the
-- request-scoped connection pinning that sets the GUC *before*
-- the first query.
--
-- # Policy shape
--
-- All user-scoped tables use:
--
--   USING (user_id = current_setting('app.user_id', true)::uuid)
--
-- The second argument `true` makes current_setting return NULL
-- (instead of raising) when the GUC isn't set; NULL::uuid =
-- user_id is NULL → false → no rows visible. That's the desired
-- closed-by-default posture for any connection that forgets to
-- set the setting.
--
-- The `users` table uses `id` (not `user_id`) as the predicate
-- column; otherwise identical. Singleton system tables
-- (`migrations`, `flags`, `push_vapid`) don't get RLS — they're
-- global config, not tenant data.

-- Helper: single place to define the predicate so adding a new
-- user-scoped table is one ENABLE + one CREATE POLICY referencing
-- the function. Marked STABLE because current_setting is stable
-- within a statement, and IMMUTABLE would let the planner inline
-- a stale value across statements.
CREATE OR REPLACE FUNCTION rivolt_current_user_id()
RETURNS UUID LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.user_id', true), '')::uuid
$$;

-- -------------------------------------------------------------------
-- users: filter by id (the only table keyed on id rather than
-- user_id). A user should only ever see their own row.
-- -------------------------------------------------------------------
ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_tenant_isolation ON users
    USING (id = rivolt_current_user_id())
    WITH CHECK (id = rivolt_current_user_id());

-- -------------------------------------------------------------------
-- user-scoped tables. One pattern repeated; extending to a new
-- table is ENABLE + CREATE POLICY using rivolt_current_user_id().
-- -------------------------------------------------------------------
DO $$
DECLARE
    t TEXT;
    tables TEXT[] := ARRAY[
        'vehicles',
        'locations',
        'charges',
        'drives',
        'vehicle_state',
        'imports',
        'push_subscriptions',
        'user_settings',
        'user_secrets',
        'sessions',
        'ai_usage'
    ];
BEGIN
    FOREACH t IN ARRAY tables LOOP
        -- Idempotent guard so a re-run (e.g. from a test harness
        -- that rewinds migrations) doesn't trip "policy already
        -- exists".
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format($f$
            DROP POLICY IF EXISTS %I ON %I;
            CREATE POLICY %I ON %I
                USING (user_id = rivolt_current_user_id())
                WITH CHECK (user_id = rivolt_current_user_id());
        $f$, t || '_tenant_isolation', t, t || '_tenant_isolation', t);
    END LOOP;
END $$;

-- NOTE: FORCE ROW LEVEL SECURITY is deliberately NOT set here.
-- That means the table owner (the DB role the app connects as
-- today) bypasses the policies. Once phase 2 ships the app-role
-- split, a follow-up migration will:
--
--   ALTER TABLE <each> FORCE ROW LEVEL SECURITY;
--   REVOKE BYPASSRLS FROM rivolt_app;
--
-- and the policies go live without touching this one.
