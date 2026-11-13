-- 0124_verify_role_grants.sql
--
-- Read-only access for the release roll's open-charge check (`rivolt open-charges`, release gitops PR 5). The role rivolt_verify is
-- created outside migrations (a CNPG managed role: LOGIN, CONNECTION LIMIT 2, no BYPASSRLS); this grants it five columns of charges and
-- nothing else, so a leaked verify credential can't read a position, a user or a payment.
--
-- charges has row-level security keyed on app.user_id, which the check never sets, so the role gets its own SELECT policy.
-- The statement timeout (5 s) is set per session by the subcommand, not here, so the role needs no ALTER ROLE.
--
-- The role may not exist yet (a fresh database, CI, a restore): the grants are then skipped with a notice, and `open-charges` fails
-- closed with a permission error until the role is created and this is re-applied by hand (infra's role runbook).
SET LOCAL lock_timeout = '2s';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'rivolt_verify') THEN
        RAISE NOTICE 'role rivolt_verify does not exist; skipping the open-charge verify grants';
        RETURN;
    END IF;
    GRANT USAGE ON SCHEMA public TO rivolt_verify;
    REVOKE ALL ON charges FROM rivolt_verify;
    GRANT SELECT (id, vehicle_id, started_at, ended_at, final_state) ON charges TO rivolt_verify;
    DROP POLICY IF EXISTS charges_verify_select ON charges;
    CREATE POLICY charges_verify_select ON charges FOR SELECT TO rivolt_verify USING (true);
END
$$;
