-- 0092_account_leases.sql — M1 (expand) of the single-owner design
-- (_shared/rivian-single-owner-design.md): one lease per user group, written
-- alongside the per-vehicle leases. Nothing reads it for a decision yet.
--
--   account_leases  group_id is the smallest user id of the #602 user group (users
--                   linked through a shared Rivian vehicle id). epoch comes from
--                   account_lease_epoch and changes on every acquire or steal, never
--                   on a renew.
--   lease_pods      one row per live pod: its template generation, when it started
--                   and when it last renewed. The rollout and fair-share rules (M2)
--                   read it.
--   rivolt_lease_ok(group, pod, epoch)
--                   the fence check every owner-side write will carry from M2. It
--                   is VOLATILE and uses statement_timestamp(), not now(), so each
--                   statement of a long transaction is checked at its own start.
--
-- Pod bookkeeping with no tenant rows (group_id is an id, not user data), so no
-- row-level security, like subscription_leases.
--
-- Rollback: DROP FUNCTION rivolt_lease_ok(uuid, text, bigint);
--   DROP TABLE account_leases, lease_pods; DROP SEQUENCE account_lease_epoch;

CREATE SEQUENCE IF NOT EXISTS account_lease_epoch;

CREATE TABLE IF NOT EXISTS account_leases (
    group_id    UUID        PRIMARY KEY,
    pod_id      TEXT        NOT NULL,
    epoch       BIGINT      NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL,
    renewed_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS account_leases_pod_idx ON account_leases (pod_id);
CREATE INDEX IF NOT EXISTS account_leases_expires_idx ON account_leases (expires_at);

CREATE TABLE IF NOT EXISTS lease_pods (
    pod_id     TEXT        PRIMARY KEY,
    generation TEXT        NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL,
    seen_at    TIMESTAMPTZ NOT NULL,
    draining   BOOLEAN     NOT NULL DEFAULT FALSE
);

CREATE OR REPLACE FUNCTION rivolt_lease_ok(g UUID, p TEXT, e BIGINT) RETURNS BOOLEAN
LANGUAGE sql VOLATILE AS $$
    SELECT EXISTS (SELECT 1 FROM account_leases
                    WHERE group_id = g AND pod_id = p AND epoch = e
                      AND expires_at > statement_timestamp())
$$;
