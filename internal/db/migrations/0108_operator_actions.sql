-- 0108_operator_actions.sql: the audited operator path for the repair CLIs (single-owner M4-4; DA's rulings on M4 design draft 1).
--
-- The repair CLIs write rows the owner also writes (charges, drives) from outside any lease, as an operator. Fencing cannot cover them, so
-- the database records and requires an audit row instead:
--   * operator_actions: one row per mutating run (tool, role label, git sha, reason, outcome, rows written). The role label is "lead" or
--     "owner", never a person's name. Restore is a run of its own.
--   * A run begins with rivolt_operator_begin() and ends with rivolt_operator_end(); the CLI then passes the row id to its connection as the
--     session setting rivolt.operator_action. The only way into those functions is the repair role (rivolt_repair, granted at boot by
--     db.GrantRepair; the role itself is created by the infra repo).
--   * A trigger on the tables the repairs write refuses every INSERT, UPDATE and DELETE by the repair role unless that setting names a row that
--     is open (not ended) and less than 12 hours old. Other roles are untouched: the WHEN clause keeps the trigger off their writes entirely.
-- Additive: one table, three functions, five triggers. Nothing is dropped, renamed or rewritten.
CREATE TABLE IF NOT EXISTS operator_actions (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    role_label  TEXT        NOT NULL CHECK (role_label IN ('lead', 'owner')),
    tool        TEXT        NOT NULL CHECK (tool <> '' AND length(tool) <= 64),
    git_sha     TEXT        NOT NULL DEFAULT '' CHECK (length(git_sha) <= 64),
    reason      TEXT        NOT NULL CHECK (reason <> '' AND length(reason) <= 300),
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ,
    outcome     TEXT        CHECK (outcome IN ('ok', 'failed')),
    -- Counted by the guard itself, one per row the run inserted, updated or deleted in any guarded table (so a backup copy counts as a row,
    -- and a rolled-back transaction counts nothing): the database's own record of how much the run changed.
    rows_written BIGINT     NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS operator_actions_started ON operator_actions (started_at);

-- No tenant column and no policy: nobody reads this through a request. It is closed to the request role (db.appDeniedTables) and reached by
-- the repair role only through the functions below.
ALTER TABLE operator_actions ENABLE ROW LEVEL SECURITY;

-- begin: the repair role opens a run and gets its id back.
CREATE OR REPLACE FUNCTION rivolt_operator_begin(p_role text, p_tool text, p_git_sha text, p_reason text)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE aid uuid;
BEGIN
    INSERT INTO operator_actions (role_label, tool, git_sha, reason)
    VALUES (p_role, p_tool, coalesce(p_git_sha, ''), p_reason)
    RETURNING id INTO aid;
    RETURN aid;
END $$;

-- end: closes the run with its outcome. A run closes once.
CREATE OR REPLACE FUNCTION rivolt_operator_end(p_id uuid, p_outcome text)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
    IF p_outcome NOT IN ('ok', 'failed') THEN
        RAISE EXCEPTION 'operator action outcome must be ok or failed' USING ERRCODE = '22023';
    END IF;
    UPDATE operator_actions SET ended_at = now(), outcome = p_outcome
     WHERE id = p_id AND ended_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'operator action % is not open', p_id USING ERRCODE = '42501';
    END IF;
END $$;

-- the guard: a repair-role write needs the session's rivolt.operator_action to name an open, recent run.
CREATE OR REPLACE FUNCTION rivolt_operator_guard()
RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE aid uuid; open_run boolean;
BEGIN
    BEGIN
        aid := nullif(current_setting('rivolt.operator_action', true), '')::uuid;
    EXCEPTION WHEN others THEN
        aid := NULL;
    END;
    IF aid IS NOT NULL THEN
        SELECT true INTO open_run FROM operator_actions
         WHERE id = aid AND ended_at IS NULL AND started_at > now() - interval '12 hours';
    END IF;
    IF open_run IS NOT TRUE THEN
        RAISE EXCEPTION 'a repair-role write on % needs an open operator action (rivolt.operator_action)', TG_TABLE_NAME USING ERRCODE = '42501';
    END IF;
    UPDATE operator_actions SET rows_written = rows_written + 1 WHERE id = aid;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END $$;

-- Closed to everyone but the roles that are granted it: PUBLIC loses the default EXECUTE. db.GrantRepair gives the repair role the
-- begin and end functions.
REVOKE ALL ON FUNCTION rivolt_operator_begin(text, text, text, text) FROM PUBLIC;
REVOKE ALL ON FUNCTION rivolt_operator_end(uuid, text) FROM PUBLIC;

DO $$
DECLARE t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['charges', 'drives', 'charges_capped_backup', 'charges_deleted_backup', 'vehicles'] LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS rivolt_operator_guard ON %I', t);
        EXECUTE format($f$
            CREATE TRIGGER rivolt_operator_guard BEFORE INSERT OR UPDATE OR DELETE ON %I
            FOR EACH ROW WHEN (current_user = 'rivolt_repair') EXECUTE FUNCTION rivolt_operator_guard()
        $f$, t);
    END LOOP;
END $$;
