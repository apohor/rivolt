-- 0094_account_leases_release.sql — a released group lease is an UPDATE, not a DELETE
-- (single-owner design 4.5): pod_id becomes NULL and released_at records when, so M2 can tell
-- how long a group has been free. Additive (a dropped NOT NULL and a nullable column); the
-- rows of 0092 stay valid.
ALTER TABLE account_leases ALTER COLUMN pod_id DROP NOT NULL;
ALTER TABLE account_leases ADD COLUMN IF NOT EXISTS released_at TIMESTAMPTZ;
