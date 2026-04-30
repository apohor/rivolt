-- 0011_subscription_leases.sql — per-vehicle ownership leases for
-- multi-replica steady state.
--
-- Rationale (ARCHITECTURE decision 5, ROADMAP Phase 2 N>1):
--
-- Today the binary calls `EnsureSubscribed(vehicleID)` for every
-- known vehicle at startup. With one replica that's correct. With
-- N replicas every pod opens its own websocket against Rivian's
-- gateway for every vehicle, multiplying upstream load by N and
-- producing duplicate state recorder writes. Rivian's rate-limit
-- and abuse-detection logic is the cluster-side blast radius.
--
-- Solution: a Postgres-backed lease table. Each row pins a vehicle
-- to exactly one pod. A pod attempts to acquire by inserting a
-- row (or stealing one whose `expires_at` is in the past); the
-- table's PRIMARY KEY guarantees only one winner. A reconciliation
-- loop in every pod renews leases it owns and tries to acquire
-- new ones it doesn't yet hold. SIGTERM clears the pod's leases
-- so a planned restart hands ownership over without waiting on
-- the TTL.
--
-- Why not Postgres advisory locks? An advisory lock dies with the
-- holding session. Pod crashes drop the lock instantly — fine —
-- but the next acquirer has no record of WHO previously owned it
-- (no audit, no debugging surface) and we can't easily express
-- "renew" without holding the same connection for the lease's
-- lifetime. A row gives us a `pod_id` we can join to logs / k8s
-- events when a vehicle starts misbehaving.
--
-- Why not Redis? Redis isn't deployed yet (separate Phase-2 line
-- item for the global token bucket). Postgres is already here.
--
-- # Columns
--
--   vehicle_id   — Rivian vehicle id; the lease subject. PK so a
--                  given vehicle is owned by at most one pod.
--   pod_id       — opaque string identifying the holder. Hostname
--                  in k8s (set via spec.template.spec.containers[].
--                  env from spec.nodeName isn't right; we want the
--                  pod name from `metadata.name` via downward API,
--                  set as RIVOLT_POD_ID by the chart).
--   acquired_at  — when this pod first won the lease (NOT updated
--                  on renew, so we can chart "lease churn").
--   renewed_at   — last successful Renew. The reconcile loop reads
--                  this to decide which leases are still alive.
--   expires_at   — `renewed_at + lease_ttl`. Stealing predicate
--                  is `expires_at < now()` so a dead pod's leases
--                  age out without explicit cleanup.

CREATE TABLE IF NOT EXISTS subscription_leases (
    vehicle_id  TEXT PRIMARY KEY,
    pod_id      TEXT NOT NULL,
    acquired_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    renewed_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ NOT NULL
);

-- Index for the renew loop's "what do I currently hold?" query and
-- the release-all path. PK already covers vehicle_id lookups.
CREATE INDEX IF NOT EXISTS subscription_leases_pod_idx
    ON subscription_leases (pod_id);

-- Index for the reconcile path's "which leases are stealable now?"
-- query. Partial index keeps it small as most rows aren't expired.
CREATE INDEX IF NOT EXISTS subscription_leases_expires_idx
    ON subscription_leases (expires_at);
